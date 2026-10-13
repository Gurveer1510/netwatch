package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Gurveer1510/netwatch/internal/ai"
	"github.com/Gurveer1510/netwatch/internal/api"
	"github.com/Gurveer1510/netwatch/internal/pipeline"
	"github.com/Gurveer1510/netwatch/internal/store"
	"github.com/redis/go-redis/v9"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		log.Printf("server failed: %v", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token := os.Getenv("NETWATCH_API_TOKEN")
	if len(token) < 24 {
		return fmt.Errorf("NETWATCH_API_TOKEN must contain at least 24 characters; run scripts/setup.sh")
	}
	aiEnabled, err := strconv.ParseBool(env("AI_ENABLED", "true"))
	if err != nil {
		return fmt.Errorf("AI_ENABLED must be true or false")
	}
	database := env("DATABASE_URL", "postgres://netwatch:netwatch@localhost:5433/netwatch?sslmode=disable")
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	s, err := store.Open(startup, database)
	cancel()
	if err != nil {
		return err
	}
	defer s.Pool.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "localhost:6380"), DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, ContextTimeoutEnabled: true, MaxRetries: -1})
	defer redisClient.Close()
	model := env("OLLAMA_MODEL", "qwen2.5:0.5b")
	web := (&api.Server{Store: s, Redis: redisClient, Token: token, AIEnabled: aiEnabled, Model: model}).Handler()
	server := &http.Server{Addr: env("HTTP_ADDR", "127.0.0.1:8080"), Handler: web, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	var wg sync.WaitGroup
	for _, c := range []*pipeline.Consumer{
		{Redis: redisClient, Store: s, Stream: "telemetry:raw", Group: "netwatch-storage", Name: fmt.Sprintf("storage-%d", os.Getpid())},
		{Redis: redisClient, Store: s, Stream: "incidents:raw", Group: "netwatch-incidents", Name: fmt.Sprintf("incidents-%d", os.Getpid()), Incidents: true, AIEnabled: aiEnabled},
	} {
		wg.Add(1)
		go func(c *pipeline.Consumer) { defer wg.Done(); c.Run(ctx) }(c)
	}
	if aiEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ai.Run(ctx, s, &ai.Client{URL: env("OLLAMA_URL", "http://localhost:11434"), Model: model, HTTP: &http.Client{Timeout: 60 * time.Second}})
		}()
	}
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServe() }()
	log.Printf("dashboard listening on %s; local AI enabled=%t", server.Addr, aiEnabled)
	select {
	case <-ctx.Done():
	case err := <-errors:
		if err != http.ErrServerClosed {
			stop()
			wg.Wait()
			return err
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = server.Shutdown(shutdown)
	wg.Wait()
	return err
}
