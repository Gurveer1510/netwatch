package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Gurveer1510/netwatch/internal/collect"
	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/gosnmp/gosnmp"
)

func main() {
	if err := run(); err != nil {
		log.Printf("collector failed: %v", err)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "linux", "linux or snmp")
	device := flag.String("device", "linux-host", "Device label")
	root := flag.String("proc-root", "/proc", "Linux proc filesystem path")
	target := flag.String("target", "127.0.0.1", "ICMP probe target; SNMP device host in snmp mode")
	api := flag.String("api-url", "http://localhost:8080", "NetWatch API base URL")
	interval := flag.Duration("interval", 10*time.Second, "Delay between collection cycles")
	count := flag.Int("count", 0, "Collection attempts; zero runs until Ctrl+C")
	cpuOID := flag.String("cpu-oid", "", "SNMP scalar CPU percentage OID")
	usedOID := flag.String("memory-used-oid", "", "SNMP scalar used memory OID")
	totalOID := flag.String("memory-total-oid", "", "SNMP scalar total memory OID (same units as used)")
	version := flag.String("snmp-version", "3", "SNMP version: 3 (recommended) or 2c")
	port := flag.Uint("snmp-port", 161, "SNMP UDP port")
	flag.Parse()
	if (*mode != "linux" && *mode != "snmp") || *device == "" || *interval <= 0 || *count < 0 || *port == 0 || *port > 65535 {
		return fmt.Errorf("invalid collector configuration")
	}
	token := os.Getenv("NETWATCH_API_TOKEN")
	if token == "" {
		return fmt.Errorf("NETWATCH_API_TOKEN is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var client *gosnmp.GoSNMP
	if *mode == "snmp" {
		if *cpuOID == "" || *usedOID == "" || *totalOID == "" {
			return fmt.Errorf("supply -cpu-oid, -memory-used-oid, and -memory-total-oid supported by your device")
		}
		var err error
		client, err = collect.ConnectSNMP(ctx, collect.SNMPConfig{
			Target:          *target,
			Port:            uint16(*port),
			Version:         *version,
			Community:       os.Getenv("SNMP_COMMUNITY"),
			User:            os.Getenv("SNMP_USER"),
			AuthPassword:    os.Getenv("SNMP_AUTH_PASSWORD"),
			PrivacyPassword: os.Getenv("SNMP_PRIV_PASSWORD"),
		})
		if err != nil {
			return err
		}
		defer client.Conn.Close()
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	for attempt := 1; ctx.Err() == nil; attempt++ {
		cpu, memory, err := read(ctx, *mode, *root, client, *cpuOID, *usedOID, *totalOID)
		var loss float64
		if err == nil {
			probe, cancel := context.WithTimeout(ctx, 10*time.Second)
			loss, err = collect.Ping(probe, *target)
			cancel()
		}
		if err == nil {
			metric := telemetry.Telemetry{DeviceID: *device, Source: *mode, Timestamp: time.Now().UTC(), CPUPercent: cpu, MemoryPercent: memory, PacketLossPct: loss}
			err = collect.Publish(ctx, httpClient, *api, token, metric)
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Printf("collection attempt=%d failed (no fabricated reading published): %v", attempt, err)
		} else {
			log.Printf("published real %s metrics device=%s cpu=%.1f%% memory=%.1f%% probe_loss=%.1f%%", *mode, *device, cpu, memory, loss)
		}
		if *count > 0 && attempt >= *count {
			if err != nil {
				return err
			}
			return nil
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
func read(ctx context.Context, mode, root string, client *gosnmp.GoSNMP, cpuOID, usedOID, totalOID string) (float64, float64, error) {
	if mode == "snmp" {
		return collect.SNMPMetrics(client, cpuOID, usedOID, totalOID)
	}
	before, err := collect.ReadCPU(root)
	if err != nil {
		return 0, 0, err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	case <-timer.C:
	}
	after, err := collect.ReadCPU(root)
	if err != nil {
		return 0, 0, err
	}
	cpu, err := collect.CPUUsage(before, after)
	if err != nil {
		return 0, 0, err
	}
	memory, err := collect.ReadMemory(root)
	return cpu, memory, err
}
