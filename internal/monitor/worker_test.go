package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/redis/go-redis/v9"
)

func TestDecode(t *testing.T) {
	valid := `{"device_id":"router-01","timestamp":"2026-10-13T12:00:00Z","cpu_percent":0,"memory_percent":40,"packet_loss_pct":0}`
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"valid zero percentages", valid, true},
		{"broken JSON", "{", false},
		{"missing metrics", `{"device_id":"router-01","timestamp":"2026-10-13T12:00:00Z"}`, false},
		{"null", `null`, false},
		{"out of range", `{"device_id":"router-01","timestamp":"2026-10-13T12:00:00Z","cpu_percent":101,"memory_percent":40,"packet_loss_pct":0}`, false},
		{"empty device", `{"device_id":" ","timestamp":"2026-10-13T12:00:00Z","cpu_percent":10,"memory_percent":40,"packet_loss_pct":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(redis.XMessage{Values: map[string]interface{}{"payload": tc.payload}})
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}

// This uses isolated temporary keys on a real Redis server. It never reads or
// deletes the application's telemetry or incidents.
func TestRedisWorker(t *testing.T) {
	addr := os.Getenv("NETWATCH_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set NETWATCH_TEST_REDIS_ADDR to run Redis integration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: addr})
	prefix := fmt.Sprintf("netwatch:test:%d:", time.Now().UnixNano())
	cfg := Config{InputStream: prefix + "telemetry", Group: "detectors", Consumer: "healthy",
		IncidentStream: prefix + "incidents", DeadLetterStream: prefix + "dead", ProcessedKey: prefix + "processed",
		CPUThreshold: 90, ClaimIdle: 10 * time.Second}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		defer client.Close()
		if err := client.Del(cleanupCtx, cfg.InputStream, cfg.IncidentStream, cfg.DeadLetterStream, cfg.ProcessedKey).Err(); err != nil {
			t.Errorf("cleanup test keys: %v", err)
		}
	})
	w, err := New(client, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.EnsureGroup(ctx); err != nil {
		t.Fatalf("existing group should be accepted: %v", err)
	}

	add := func(cpu float64, raw string) redis.XMessage {
		t.Helper()
		if raw == "" {
			body, err := json.Marshal(telemetry.Telemetry{Source: "windows", DeviceID: "router-01", Timestamp: time.Now().UTC(), CPUPercent: cpu, MemoryPercent: 40, PacketLossPct: 0})
			if err != nil {
				t.Fatal(err)
			}
			raw = string(body)
		}
		id, err := client.XAdd(ctx, &redis.XAddArgs{Stream: cfg.InputStream, Values: map[string]interface{}{"payload": raw}}).Result()
		if err != nil {
			t.Fatal(err)
		}
		streams, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: cfg.Group, Consumer: "crashed", Streams: []string{cfg.InputStream, ">"}, Count: 1}).Result()
		if err != nil {
			t.Fatal(err)
		}
		if len(streams) != 1 || len(streams[0].Messages) != 1 || streams[0].Messages[0].ID != id {
			t.Fatal("unexpected delivery")
		}
		return streams[0].Messages[0]
	}
	checkProcess := func(msg redis.XMessage, want string) {
		t.Helper()
		got, err := w.Process(ctx, msg)
		if err != nil || got != want {
			t.Fatalf("Process = %q, %v; want %q", got, err, want)
		}
	}

	checkProcess(add(90, ""), "normal") // Strictly greater than the threshold.
	high := add(95, "")
	checkProcess(high, "incident")
	checkProcess(high, "duplicate")
	if n := client.XLen(ctx, cfg.IncidentStream).Val(); n != 1 {
		t.Fatalf("duplicate incident: count=%d", n)
	}
	entries, err := client.XRange(ctx, cfg.IncidentStream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	var incident Incident
	if err := json.Unmarshal([]byte(entries[0].Values["payload"].(string)), &incident); err != nil {
		t.Fatal(err)
	}
	if incident.SourceEventID != high.ID || incident.CPUPercent != 95 || incident.DeviceID != "router-01" || incident.Source != "windows" {
		t.Fatalf("wrong incident evidence: %+v", incident)
	}
	bad := add(0, "{invalid")
	checkProcess(bad, "dead-letter")
	checkProcess(bad, "duplicate")
	if n := client.XLen(ctx, cfg.DeadLetterStream).Val(); n != 1 {
		t.Fatalf("expected one dead letter; got %d", n)
	}
	if pending := client.XPending(ctx, cfg.InputStream, cfg.Group).Val(); pending.Count != 0 {
		t.Fatal("completed events were not acknowledged")
	}

	// A failed output write must not acknowledge or mark the input processed.
	failure := add(96, "")
	if err := client.Del(ctx, cfg.IncidentStream).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, cfg.IncidentStream, "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Process(ctx, failure); err == nil {
		t.Fatal("expected storage failure")
	}
	if pending := client.XPending(ctx, cfg.InputStream, cfg.Group).Val(); pending.Count != 1 {
		t.Fatal("failed input must remain pending")
	}
	if client.HExists(ctx, cfg.ProcessedKey, failure.ID).Val() {
		t.Fatal("failed write must not be marked processed")
	}
	if err := client.Del(ctx, cfg.IncidentStream).Err(); err != nil {
		t.Fatal(err)
	}
	checkProcess(failure, "incident")

	// Simulate an abandoned delivery with idle time > ClaimIdle, without
	// making the test actually sleep for ten seconds.
	abandoned := add(97, "")
	if err := client.Do(ctx, "XCLAIM", cfg.InputStream, cfg.Group, "crashed", 0, abandoned.ID, "IDLE", 11000, "JUSTID").Err(); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			stop()
			<-done
			t.Fatal("worker did not recover abandoned event")
		case <-poll.C:
			if client.HExists(ctx, cfg.ProcessedKey, abandoned.ID).Val() {
				stop()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if pending := client.XPending(ctx, cfg.InputStream, cfg.Group).Val(); pending.Count != 0 {
					t.Fatal("recovered event must be acknowledged")
				}
				if n := client.XLen(ctx, cfg.IncidentStream).Val(); n != 2 {
					t.Fatalf("expected failed-write recovery and abandoned-event incidents, got %d", n)
				}
				return
			}
		}
	}
}
