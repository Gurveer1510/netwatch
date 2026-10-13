package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Gurveer1510/netwatch/internal/monitor"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		log.Printf("worker failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("redis-addr", "localhost:6380", "Redis host:port")
	stream := flag.String("stream", "telemetry:raw", "Input telemetry stream")
	group := flag.String("group", "netwatch-detectors", "Consumer group")
	consumer := flag.String("consumer", fmt.Sprintf("worker-%d", os.Getpid()), "Unique name for this worker")
	incidents := flag.String("incidents", "incidents:raw", "Output incident stream")
	deadLetters := flag.String("dead-letters", "telemetry:dead", "Invalid telemetry stream")
	threshold := flag.Float64("cpu-threshold", 90, "Create an incident when CPU exceeds this percentage")
	claimIdle := flag.Duration("claim-idle", 30*time.Second, "Recover messages idle this long (minimum 10s)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := redis.NewClient(&redis.Options{
		Addr: *addr, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
		ContextTimeoutEnabled: true, MaxRetries: -1,
	})
	defer client.Close()
	worker, err := monitor.New(client, monitor.Config{
		InputStream: *stream, Group: *group, Consumer: *consumer,
		IncidentStream: *incidents, DeadLetterStream: *deadLetters,
		ProcessedKey: *stream + ":processed:" + *group,
		CPUThreshold: *threshold, ClaimIdle: *claimIdle,
	})
	if err != nil {
		return err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = client.Ping(pingCtx).Err()
	cancel()
	if err != nil {
		return fmt.Errorf("connect to Redis: %w; start docker compose up -d", err)
	}
	return worker.Run(ctx)
}
