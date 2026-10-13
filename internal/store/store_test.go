package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Gurveer1510/netwatch/internal/monitor"
	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPersistenceAndAnalysisQueue(t *testing.T) {
	databaseURL := os.Getenv("NETWATCH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set NETWATCH_TEST_DATABASE_URL for PostgreSQL integration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("netwatch_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var s *Store
	t.Cleanup(func() {
		if s != nil {
			s.Pool.Close()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
		admin.Close()
	})
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schemaName)
	u.RawQuery = query.Encode()
	s, err = Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	metric := telemetry.Telemetry{DeviceID: "live-device", Source: "linux", Timestamp: time.Now().UTC(), CPUPercent: 95, MemoryPercent: 40, PacketLossPct: 0}
	for i := 0; i < 2; i++ {
		if err := s.SaveTelemetry(ctx, "test:telemetry", "1-0", metric); err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.History(ctx, "live-device", 10)
	if err != nil || len(history) != 1 || history[0].Source != "linux" {
		t.Fatalf("history=%+v %v", history, err)
	}
	devices, err := s.Devices(ctx)
	if err != nil || len(devices) != 1 || devices[0].Stale {
		t.Fatalf("devices=%+v %v", devices, err)
	}
	incident := monitor.Incident{ID: "test:incident", DeviceID: metric.DeviceID, Source: "linux", CPUPercent: 95, Threshold: 90, ObservedAt: metric.Timestamp, DetectedAt: metric.Timestamp}
	for i := 0; i < 2; i++ {
		if err := s.SaveIncident(ctx, incident, true); err != nil {
			t.Fatal(err)
		}
	}
	claimed, attempt, err := s.ClaimAnalysis(ctx)
	if err != nil || claimed.ID != incident.ID || attempt != 1 {
		t.Fatalf("claim=%+v attempt=%d %v", claimed, attempt, err)
	}
	if _, _, err := s.ClaimAnalysis(ctx); !IsEmpty(err) {
		t.Fatalf("active lease reclaimed: %v", err)
	}
	if err := s.CompleteAnalysis(ctx, incident.ID, attempt, []byte(`{"summary":"Cause unconfirmed"}`)); err != nil {
		t.Fatal(err)
	}
	items, err := s.Incidents(ctx, "open", 10)
	if err != nil || len(items) != 1 || items[0].AnalysisStatus != "complete" || !json.Valid(items[0].Analysis) {
		t.Fatalf("incident=%+v %v", items, err)
	}
	if ok, err := s.SetStatus(ctx, incident.ID, "resolved"); err != nil || !ok {
		t.Fatal("status update failed")
	}
	if ok, err := s.RetryAnalysis(ctx, incident.ID); err != nil || !ok {
		t.Fatal("analysis retry failed")
	}
	if _, _, err := s.ClaimAnalysis(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a server crash by expiring the lease.
	if _, err := s.Pool.Exec(ctx, "UPDATE incidents SET locked_until=now()-interval '1 second' WHERE id=$1", incident.ID); err != nil {
		t.Fatal(err)
	}
	_, attempt, err = s.ClaimAnalysis(ctx)
	if err != nil || attempt != 2 {
		t.Fatalf("lease recovery attempt=%d %v", attempt, err)
	}
	if err := s.FailAnalysis(ctx, incident.ID, 3, "model unavailable"); err != nil {
		t.Fatal(err)
	}
	// The stale attempt must not overwrite the active attempt.
	items, err = s.Incidents(ctx, "resolved", 10)
	if err != nil || items[0].AnalysisStatus != "running" {
		t.Fatal("stale completion changed active lease")
	}
	if err := s.FailAnalysis(ctx, incident.ID, attempt, "model unavailable"); err != nil {
		t.Fatal(err)
	}
	items, err = s.Incidents(ctx, "resolved", 10)
	if err != nil || items[0].AnalysisStatus != "pending" {
		t.Fatal("failure not queued for retry")
	}
}
