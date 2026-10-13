package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/gosnmp/gosnmp"
)

func TestLinuxCounters(t *testing.T) {
	before, err := ParseCPU(strings.NewReader("cpu 100 0 50 800 50 0 0 0 40 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseCPU(strings.NewReader("cpu 150 0 50 850 50 0 0 0 60 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	usage, err := CPUUsage(before, after)
	if err != nil || usage != 50 {
		t.Fatalf("CPU=%f %v", usage, err)
	}
	memory, err := ParseMemory(strings.NewReader("MemTotal: 1000 kB\nMemAvailable: 250 kB\n"))
	if err != nil || memory != 75 {
		t.Fatalf("memory=%f %v", memory, err)
	}
	if _, err := CPUUsage(after, before); err == nil {
		t.Fatal("counter reset accepted")
	}
	if _, err := ParseMemory(strings.NewReader("MemTotal: 1000 kB\n")); err == nil {
		t.Fatal("missing available memory accepted")
	}
}
func TestProbeLoss(t *testing.T) {
	for _, want := range []float64{0, 40, 100} {
		text := map[float64]string{0: "5 packets transmitted, 5 received, 0% packet loss", 40: "5 packets transmitted, 3 received, 40% packet loss", 100: "5 packets transmitted, 0 received, 100% packet loss"}[want]
		got, err := ParseLoss(text)
		if err != nil || got != want {
			t.Fatalf("loss=%f %v", got, err)
		}
	}
	if _, err := ParseLoss("ping: unknown host"); err == nil {
		t.Fatal("failed ping converted to fake metrics")
	}
}

type fakeSNMP struct{ packet *gosnmp.SnmpPacket }

func (f fakeSNMP) Get([]string) (*gosnmp.SnmpPacket, error) { return f.packet, nil }
func TestSNMPMetrics(t *testing.T) {
	packet := &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{{Type: gosnmp.Integer, Value: 65}, {Type: gosnmp.Gauge32, Value: uint32(300)}, {Type: gosnmp.Gauge32, Value: uint32(1000)}}}
	cpu, memory, err := SNMPMetrics(fakeSNMP{packet}, ".1", ".2", ".3")
	if err != nil || cpu != 65 || memory != 30 {
		t.Fatalf("SNMP=%f %f %v", cpu, memory, err)
	}
	packet.Variables[0].Type = gosnmp.NoSuchInstance
	if _, _, err := SNMPMetrics(fakeSNMP{packet}, ".1", ".2", ".3"); err == nil {
		t.Fatal("missing OID accepted")
	}
}
func TestAuthenticatedPublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/telemetry" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong endpoint or authentication")
		}
		var m telemetry.Telemetry
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Error(err)
		}
		if m.Source != "linux" {
			t.Error("source missing")
		}
		w.WriteHeader(202)
	}))
	defer server.Close()
	if err := Publish(context.Background(), server.Client(), server.URL, "secret", telemetry.Telemetry{Source: "linux"}); err != nil {
		t.Fatal(err)
	}
}
