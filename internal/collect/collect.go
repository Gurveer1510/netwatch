package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/gosnmp/gosnmp"
)

type CPU struct{ Total, Idle uint64 }

func ParseCPU(reader io.Reader) (CPU, error) {
	scan := bufio.NewScanner(reader)
	if !scan.Scan() {
		return CPU{}, fmt.Errorf("missing /proc/stat CPU line")
	}
	fields := strings.Fields(scan.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return CPU{}, fmt.Errorf("invalid aggregate CPU line")
	}
	var result CPU
	// The first eight counters include user through steal. guest counters are
	// already included in user/nice and must not be counted twice.
	for i := 1; i < len(fields) && i <= 8; i++ {
		n, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return CPU{}, err
		}
		result.Total += n
		if i == 4 || i == 5 {
			result.Idle += n
		}
	}
	return result, nil
}

func CPUUsage(before, after CPU) (float64, error) {
	if after.Total <= before.Total || after.Idle < before.Idle {
		return 0, fmt.Errorf("CPU counters did not advance or reset")
	}
	total, idle := after.Total-before.Total, after.Idle-before.Idle
	if idle > total {
		return 0, fmt.Errorf("invalid CPU counter delta")
	}
	return 100 * float64(total-idle) / float64(total), nil
}

func ParseMemory(reader io.Reader) (float64, error) {
	values := map[string]uint64{}
	scan := bufio.NewScanner(reader)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) >= 2 && (fields[0] == "MemTotal:" || fields[0] == "MemAvailable:") {
			n, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			values[fields[0]] = n
		}
	}
	if err := scan.Err(); err != nil {
		return 0, err
	}
	total, ok := values["MemTotal:"]
	available, hasAvailable := values["MemAvailable:"]
	if !ok || !hasAvailable || total == 0 || available > total {
		return 0, fmt.Errorf("MemTotal and MemAvailable are required")
	}
	return 100 * float64(total-available) / float64(total), nil
}

func ReadCPU(root string) (CPU, error) {
	f, err := os.Open(filepath.Join(root, "stat"))
	if err != nil {
		return CPU{}, err
	}
	defer f.Close()
	return ParseCPU(f)
}
func ReadMemory(root string) (float64, error) {
	f, err := os.Open(filepath.Join(root, "meminfo"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return ParseMemory(f)
}

var lossPattern = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)% packet loss`)

func ParseLoss(output string) (float64, error) {
	match := lossPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return 0, fmt.Errorf("ping did not return a packet-loss summary")
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil || value < 0 || value > 100 {
		return 0, fmt.Errorf("invalid ping loss")
	}
	return value, nil
}

// Ping is a read-only ICMP probe from this collector to target. It is not an
// interface discard rate or a measurement of all traffic through a router.
func Ping(ctx context.Context, target string) (float64, error) {
	if target == "" || strings.HasPrefix(target, "-") {
		return 0, fmt.Errorf("invalid ping target")
	}
	command := exec.CommandContext(ctx, "ping", "-n", "-c", "5", "-W", "1", target)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	loss, parseErr := ParseLoss(string(output))
	if parseErr != nil {
		return 0, fmt.Errorf("ping failed (%v): %w", err, parseErr)
	}
	return loss, nil // ping exit 1 can represent genuine 100% measured loss.
}

type SNMPGetter interface {
	Get([]string) (*gosnmp.SnmpPacket, error)
}

func SNMPMetrics(client SNMPGetter, cpuOID, usedOID, totalOID string) (float64, float64, error) {
	if cpuOID == "" || usedOID == "" || totalOID == "" {
		return 0, 0, fmt.Errorf("SNMP requires CPU, memory-used, and memory-total OIDs for your device")
	}
	packet, err := client.Get([]string{cpuOID, usedOID, totalOID})
	if err != nil {
		return 0, 0, err
	}
	if packet == nil {
		return 0, 0, fmt.Errorf("SNMP returned no packet")
	}
	if packet.Error != gosnmp.NoError || len(packet.Variables) != 3 {
		return 0, 0, fmt.Errorf("SNMP returned an error or missing OIDs")
	}
	values := make([]float64, 3)
	for i, v := range packet.Variables {
		if v.Type == gosnmp.NoSuchObject || v.Type == gosnmp.NoSuchInstance || v.Type == gosnmp.EndOfMibView {
			return 0, 0, fmt.Errorf("device does not expose requested OID %s", v.Name)
		}
		value, err := strconv.ParseFloat(fmt.Sprint(v.Value), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, 0, fmt.Errorf("OID %s is not a finite number", v.Name)
		}
		values[i] = value
	}
	if values[0] < 0 || values[0] > 100 || values[2] <= 0 || values[1] < 0 || values[1] > values[2] {
		return 0, 0, fmt.Errorf("invalid SNMP CPU or memory values; verify OIDs and matching memory units")
	}
	return values[0], 100 * values[1] / values[2], nil
}

func Publish(ctx context.Context, httpClient *http.Client, endpoint, token string, m telemetry.Telemetry) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/api/telemetry", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("telemetry API returned HTTP %d", response.StatusCode)
	}
	return nil
}
