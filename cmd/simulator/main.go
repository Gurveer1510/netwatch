package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Gurveer1510/netwatch/internal/telemetry"
	"github.com/redis/go-redis/v9"
)

// Telemetry uses the shared schema consumed by the detection worker.
type Telemetry = telemetry.Telemetry

func main() {
	if err := run(); err != nil {
		log.Printf("simulator failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("redis-addr", "localhost:6380", "Redis host:port")
	stream := flag.String("stream", "telemetry:raw", "Redis Stream name")
	device := flag.String("device", "router-01", "Simulated device ID")
	interval := flag.Duration("interval", 5*time.Second, "Time between readings")
	count := flag.Int("count", 0, "Number of readings to attempt; 0 runs until Ctrl+C")
	flag.Parse()
	if *interval <= 0 || *count < 0 || *addr == "" || *stream == "" || *device == "" {
		return fmt.Errorf("interval must be positive, count nonnegative, and address, stream, device nonempty")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := redis.NewClient(&redis.Options{
		Addr:                  *addr,
		DialTimeout:           3 * time.Second,
		ReadTimeout:           3 * time.Second,
		WriteTimeout:          3 * time.Second,
		ContextTimeoutEnabled: true,
		// A write can succeed even if its response is lost. Avoid automatic
		// retries here; a later consumer must still handle duplicate events.
		MaxRetries: -1,
	})
	defer client.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := client.Ping(pingCtx).Err()
	cancel()
	if err != nil {
		return fmt.Errorf("connect to Redis at %s: %w (start it with docker compose up -d)", *addr, err)
	}
	log.Printf("connected to Redis; device=%s stream=%s interval=%s", *device, *stream, *interval)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for reading := 1; ; reading++ {
		select {
		case <-ctx.Done():
			log.Print("shutdown requested; simulator stopped")
			return nil
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return nil
		}
		metric := generateTelemetry(*device, reading, time.Now())
		payload, err := json.Marshal(metric)
		if err != nil {
			return fmt.Errorf("encode telemetry: %w", err)
		}
		writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		id, err := client.XAdd(writeCtx, &redis.XAddArgs{
			Stream: *stream,
			Values: map[string]interface{}{"payload": string(payload)},
		}).Result()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				log.Print("shutdown requested; simulator stopped")
				return nil
			}
			// Failed readings are logged and skipped in this learning version.
			log.Printf("publish failed reading=%d: %v", reading, err)
		} else {
			log.Printf("published id=%s reading=%d cpu=%.1f%% memory=%.1f%% packet_loss=%.2f%%", id, reading, metric.CPUPercent, metric.MemoryPercent, metric.PacketLossPct)
		}
		if *count > 0 && reading >= *count {
			log.Printf("finished %d reading attempts", *count)
			return nil
		}
	}
}

func generateTelemetry(deviceID string, reading int, now time.Time) Telemetry {
	cpu := 20 + rand.Float64()*30
	if reading%6 == 0 {
		cpu = 92 + rand.Float64()*6
	}
	return Telemetry{
		Source:        "simulated",
		DeviceID:      deviceID,
		Timestamp:     now.UTC(),
		CPUPercent:    cpu,
		MemoryPercent: 30 + rand.Float64()*30,
		PacketLossPct: rand.Float64(),
	}
}
