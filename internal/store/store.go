package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Gurveer1510/netwatch/internal/monitor"
	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	s := &Store{Pool: pool}
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(1791913001)"); err == nil {
		_, err = tx.Exec(ctx, schema)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return s, nil
}

func (s *Store) SaveTelemetry(ctx context.Context, stream, id string, m telemetry.Telemetry) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO telemetry(source_stream,event_id,device_id,source,observed_at,cpu_percent,memory_percent,packet_loss_pct)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, stream, id, m.DeviceID, m.Source, m.Timestamp, m.CPUPercent, m.MemoryPercent, m.PacketLossPct)
	return err
}

func (s *Store) SaveIncident(ctx context.Context, incident monitor.Incident, aiEnabled bool) error {
	body, err := json.Marshal(incident)
	if err != nil {
		return err
	}
	state := "disabled"
	if aiEnabled {
		state = "pending"
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO incidents(id,device_id,observed_at,payload,analysis_status) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, incident.ID, incident.DeviceID, incident.ObservedAt, body, state)
	return err
}

type Device struct {
	telemetry.Telemetry
	LastSeen time.Time `json:"last_seen"`
	Stale    bool      `json:"stale"`
}

func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (device_id) device_id,source,observed_at,cpu_percent,memory_percent,packet_loss_pct,ingested_at FROM telemetry ORDER BY device_id,observed_at DESC,ingested_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := make([]Device, 0)
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.DeviceID, &d.Source, &d.Timestamp, &d.CPUPercent, &d.MemoryPercent, &d.PacketLossPct, &d.LastSeen); err != nil {
			return nil, err
		}
		d.Stale = time.Since(d.LastSeen) > 30*time.Second
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *Store) History(ctx context.Context, device string, limit int) ([]telemetry.Telemetry, error) {
	return s.history(ctx, device, limit, nil)
}

// HistoryAt supplies only observations at or before the incident to the model.
func (s *Store) HistoryAt(ctx context.Context, device string, limit int, at time.Time) ([]telemetry.Telemetry, error) {
	return s.history(ctx, device, limit, &at)
}

func (s *Store) history(ctx context.Context, device string, limit int, cutoff *time.Time) ([]telemetry.Telemetry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT device_id,source,observed_at,cpu_percent,memory_percent,packet_loss_pct FROM telemetry WHERE device_id=$1 AND ($3::timestamptz IS NULL OR observed_at <= $3) ORDER BY observed_at DESC,ingested_at DESC LIMIT $2`, device, limit, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	metrics := make([]telemetry.Telemetry, 0)
	for rows.Next() {
		var m telemetry.Telemetry
		if err := rows.Scan(&m.DeviceID, &m.Source, &m.Timestamp, &m.CPUPercent, &m.MemoryPercent, &m.PacketLossPct); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

type IncidentRecord struct {
	monitor.Incident
	Status         string          `json:"status"`
	AnalysisStatus string          `json:"analysis_status"`
	Analysis       json.RawMessage `json:"analysis"`
	AnalysisError  string          `json:"analysis_error"`
}

func (s *Store) Incidents(ctx context.Context, status string, limit int) ([]IncidentRecord, error) {
	rows, err := s.Pool.Query(ctx, `SELECT payload,status,analysis_status,analysis,analysis_error FROM incidents WHERE ($1='' OR status=$1) ORDER BY observed_at DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]IncidentRecord, 0)
	for rows.Next() {
		var r IncidentRecord
		var body []byte
		if err := rows.Scan(&body, &r.Status, &r.AnalysisStatus, &r.Analysis, &r.AnalysisError); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &r.Incident); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

func (s *Store) SetStatus(ctx context.Context, id, status string) (bool, error) {
	result, err := s.Pool.Exec(ctx, "UPDATE incidents SET status=$2 WHERE id=$1", id, status)
	return result.RowsAffected() > 0, err
}

func (s *Store) RetryAnalysis(ctx context.Context, id string) (bool, error) {
	result, err := s.Pool.Exec(ctx, `UPDATE incidents SET analysis_status='pending',attempts=0,next_attempt=now(),analysis_error='',analysis=NULL,locked_until=NULL WHERE id=$1 AND (analysis_status!='running' OR locked_until<now())`, id)
	return result.RowsAffected() > 0, err
}

// ClaimAnalysis leases one job. A stopped server's lease expires, permitting
// another server to recover it. SKIP LOCKED allows safe concurrent servers.
func (s *Store) ClaimAnalysis(ctx context.Context) (monitor.Incident, int, error) {
	var incident monitor.Incident
	var body []byte
	var attempt int
	err := s.Pool.QueryRow(ctx, `UPDATE incidents SET analysis_status='running',attempts=attempts+1,locked_until=now()+interval '90 seconds'
 WHERE id=(SELECT id FROM incidents WHERE (analysis_status='pending' AND next_attempt<=now()) OR (analysis_status='running' AND locked_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
 RETURNING payload,attempts`).Scan(&body, &attempt)
	if err != nil {
		return incident, 0, err
	}
	err = json.Unmarshal(body, &incident)
	return incident, attempt, err
}

func (s *Store) CompleteAnalysis(ctx context.Context, id string, attempt int, body []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE incidents SET analysis_status='complete',analysis=$3,analysis_error='',locked_until=NULL WHERE id=$1 AND attempts=$2 AND analysis_status='running'`, id, attempt, body)
	return err
}

func (s *Store) FailAnalysis(ctx context.Context, id string, attempt int, reason string) error {
	state := "pending"
	if attempt >= 3 {
		state = "error"
	}
	_, err := s.Pool.Exec(ctx, `UPDATE incidents SET analysis_status=$3,analysis_error=$4,locked_until=NULL,next_attempt=now()+($5 * interval '1 second') WHERE id=$1 AND attempts=$2 AND analysis_status='running'`, id, attempt, state, reason, attempt*15)
	return err
}

func IsEmpty(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
