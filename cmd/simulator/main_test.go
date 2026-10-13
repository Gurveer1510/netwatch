package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTelemetryScenario(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for reading := 1; reading <= 60; reading++ {
		m := generateTelemetry("router-01", reading, now)
		if m.DeviceID != "router-01" || !m.Timestamp.Equal(now) {
			t.Fatal("device identity or timestamp lost")
		}
		low, high := 20.0, 50.0
		if reading%6 == 0 {
			low, high = 92, 98
		}
		if m.CPUPercent < low || m.CPUPercent >= high {
			t.Fatalf("reading %d CPU outside scenario bounds: %f", reading, m.CPUPercent)
		}
		if m.MemoryPercent < 30 || m.MemoryPercent >= 60 || m.PacketLossPct < 0 || m.PacketLossPct >= 1 {
			t.Fatal("memory or packet loss outside expected bounds")
		}
		payload, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Telemetry
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != m {
			t.Fatal("telemetry changed during JSON round trip")
		}
	}
}
