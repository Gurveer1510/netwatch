package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	InputStream      string
	Group            string
	Consumer         string
	IncidentStream   string
	DeadLetterStream string
	ProcessedKey     string
	CPUThreshold     float64
	ClaimIdle        time.Duration
}

type Incident struct {
	Source        string    `json:"source"`
	ID            string    `json:"id"`
	DeviceID      string    `json:"device_id"`
	SourceStream  string    `json:"source_stream"`
	SourceEventID string    `json:"source_event_id"`
	Rule          string    `json:"rule"`
	Severity      string    `json:"severity"`
	CPUPercent    float64   `json:"cpu_percent"`
	Threshold     float64   `json:"threshold"`
	ObservedAt    time.Time `json:"observed_at"`
	DetectedAt    time.Time `json:"detected_at"`
}

type Worker struct {
	client *redis.Client
	cfg    Config
}

func New(client *redis.Client, cfg Config) (*Worker, error) {
	if cfg.InputStream == "" || cfg.Group == "" || cfg.Consumer == "" || cfg.IncidentStream == "" || cfg.DeadLetterStream == "" || cfg.ProcessedKey == "" {
		return nil, fmt.Errorf("stream, group, consumer, and storage names must not be empty")
	}
	keys := []string{cfg.InputStream, cfg.IncidentStream, cfg.DeadLetterStream, cfg.ProcessedKey}
	for i, key := range keys {
		for _, other := range keys[i+1:] {
			if key == other {
				return nil, fmt.Errorf("input, incidents, dead letters, and processed keys must be distinct")
			}
		}
	}
	if math.IsNaN(cfg.CPUThreshold) || math.IsInf(cfg.CPUThreshold, 0) || cfg.CPUThreshold < 0 || cfg.CPUThreshold > 100 {
		return nil, fmt.Errorf("CPU threshold must be between 0 and 100")
	}
	if cfg.ClaimIdle < 10*time.Second {
		return nil, fmt.Errorf("claim idle must be at least 10s to allow active processing to finish")
	}
	return &Worker{client: client, cfg: cfg}, nil
}

// Start at 0 so the first worker also processes Phase 1's retained readings.
func (w *Worker) EnsureGroup(ctx context.Context) error {
	err := w.client.XGroupCreateMkStream(ctx, w.cfg.InputStream, w.cfg.Group, "0").Err()
	if err != nil && !redis.HasErrorPrefix(err, "BUSYGROUP") {
		return err
	}
	return nil
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.EnsureGroup(ctx); err != nil {
		return fmt.Errorf("create consumer group: %w", err)
	}
	log.Printf("worker started consumer=%s group=%s stream=%s CPU rule=>%.1f%%", w.cfg.Consumer, w.cfg.Group, w.cfg.InputStream, w.cfg.CPUThreshold)
	nextClaim := time.Now()
	claimCursor := "0-0"
	for ctx.Err() == nil {
		if !time.Now().Before(nextClaim) {
			// Claim idle pending entries, including entries left by this worker.
			// Preserve the cursor to scan the whole pending list across iterations.
			claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			messages, cursor, err := w.client.XAutoClaim(claimCtx, &redis.XAutoClaimArgs{
				Stream: w.cfg.InputStream, Group: w.cfg.Group, Consumer: w.cfg.Consumer,
				MinIdle: w.cfg.ClaimIdle, Start: claimCursor, Count: 20,
			}).Result()
			cancel()
			if err != nil && err != redis.Nil {
				w.handleError(ctx, err)
			} else {
				claimCursor = cursor
				for _, msg := range messages {
					w.processAndLog(ctx, msg, true)
				}
			}
			nextClaim = time.Now().Add(5 * time.Second)
		}
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		streams, err := w.client.XReadGroup(readCtx, &redis.XReadGroupArgs{
			Group: w.cfg.Group, Consumer: w.cfg.Consumer,
			Streams: []string{w.cfg.InputStream, ">"}, Count: 20, Block: time.Second,
		}).Result()
		cancel()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			w.handleError(ctx, err)
			continue
		}
		for _, stream := range streams {
			for _, msg := range stream.Messages {
				if ctx.Err() != nil {
					return nil
				}
				w.processAndLog(ctx, msg, false)
			}
		}
	}
	log.Print("worker stopped")
	return nil
}

func (w *Worker) handleError(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	log.Printf("Redis operation failed: %v; retrying", err)
	if redis.HasErrorPrefix(err, "NOGROUP") {
		claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := w.EnsureGroup(claimCtx); err != nil {
			log.Printf("recreate group: %v", err)
		}
		cancel()
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (w *Worker) processAndLog(ctx context.Context, msg redis.XMessage, recovered bool) {
	workCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := w.Process(workCtx, msg)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("processing failed event=%s (left pending): %v", msg.ID, err)
		}
		return
	}
	log.Printf("processed event=%s result=%s recovered=%t", msg.ID, result, recovered)
}

// Redis runs this script without interleaving other commands. Retaining the
// processed IDs prevents duplicate output when a message is delivered again.
// Check key types before writes: Lua runtime errors do not roll back prior writes.
var finish = redis.NewScript(`
local function check_type(key, expected)
  local actual = redis.call('TYPE', key).ok
  if actual ~= 'none' and actual ~= expected then
    return redis.error_reply('WRONGTYPE expected ' .. expected .. ' for ' .. key)
  end
end
local err = check_type(KEYS[1], 'stream')
if err then return err end
err = check_type(KEYS[2], 'hash')
if err then return err end
if ARGV[3] ~= 'normal' then
  err = check_type(KEYS[3], 'stream')
  if err then return err end
end
local prior = redis.call('HGET', KEYS[2], ARGV[2])
if prior then
  redis.call('XACK', KEYS[1], ARGV[1], ARGV[2])
  return 'duplicate'
end
if ARGV[3] ~= 'normal' then
  redis.call('XADD', KEYS[3], '*', 'payload', ARGV[4], 'source_event_id', ARGV[2])
end
redis.call('HSET', KEYS[2], ARGV[2], ARGV[3])
redis.call('XACK', KEYS[1], ARGV[1], ARGV[2])
return ARGV[3]
`)

// Process persists an outcome and acknowledges its source message. Redis errors
// leave the message pending so it can be recovered rather than silently lost.
func (w *Worker) Process(ctx context.Context, msg redis.XMessage) (string, error) {
	metric, err := Decode(msg)
	result, output, payload := "normal", w.cfg.IncidentStream, ""
	if err != nil {
		result, output = "dead-letter", w.cfg.DeadLetterStream
		body, marshalErr := json.Marshal(struct {
			SourceStream  string                 `json:"source_stream"`
			SourceEventID string                 `json:"source_event_id"`
			Error         string                 `json:"error"`
			Original      map[string]interface{} `json:"original"`
		}{w.cfg.InputStream, msg.ID, err.Error(), msg.Values})
		if marshalErr != nil {
			return "", marshalErr
		}
		payload = string(body)
	} else if metric.CPUPercent > w.cfg.CPUThreshold {
		result = "incident"
		incident := Incident{
			Source:   metric.Source,
			ID:       w.cfg.InputStream + ":" + msg.ID + ":high_cpu",
			DeviceID: metric.DeviceID, SourceStream: w.cfg.InputStream, SourceEventID: msg.ID,
			Rule: "high_cpu", Severity: "warning", CPUPercent: metric.CPUPercent,
			Threshold: w.cfg.CPUThreshold, ObservedAt: metric.Timestamp.UTC(), DetectedAt: time.Now().UTC(),
		}
		body, err := json.Marshal(incident)
		if err != nil {
			return "", err
		}
		payload = string(body)
	}
	return finish.Run(ctx, w.client, []string{w.cfg.InputStream, w.cfg.ProcessedKey, output}, w.cfg.Group, msg.ID, result, payload).Text()
}

func Decode(msg redis.XMessage) (telemetry.Telemetry, error) {
	var metric telemetry.Telemetry
	raw, ok := msg.Values["payload"].(string)
	if !ok {
		return metric, fmt.Errorf("payload must be a JSON string")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return metric, fmt.Errorf("invalid JSON: %w", err)
	}
	for _, name := range []string{"device_id", "timestamp", "cpu_percent", "memory_percent", "packet_loss_pct"} {
		value, ok := fields[name]
		if !ok || string(value) == "null" {
			return metric, fmt.Errorf("missing or null field: %s", name)
		}
	}
	if err := json.Unmarshal([]byte(raw), &metric); err != nil {
		return metric, fmt.Errorf("invalid telemetry: %w", err)
	}
	if strings.TrimSpace(metric.DeviceID) == "" || metric.Timestamp.IsZero() {
		return metric, fmt.Errorf("device_id and timestamp are required")
	}
	for _, value := range []float64{metric.CPUPercent, metric.MemoryPercent, metric.PacketLossPct} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return metric, fmt.Errorf("percentages must be between 0 and 100")
		}
	}
	if metric.Source == "" {
		metric.Source = "unknown"
	}
	switch metric.Source {
	case "unknown", "simulated", "linux", "windows", "snmp", "external":
	default:
		return metric, fmt.Errorf("unsupported metric source")
	}
	return metric, nil
}
