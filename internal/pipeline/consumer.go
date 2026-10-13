package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Gurveer1510/netwatch/internal/monitor"
	"github.com/Gurveer1510/netwatch/internal/store"
	"github.com/redis/go-redis/v9"
)

type Consumer struct {
	Redis               *redis.Client
	Store               *store.Store
	Stream, Group, Name string
	Incidents           bool
	AIEnabled           bool
}

func (c *Consumer) ensure(ctx context.Context) error {
	err := c.Redis.XGroupCreateMkStream(ctx, c.Stream, c.Group, "0").Err()
	if redis.HasErrorPrefix(err, "BUSYGROUP") {
		return nil
	}
	return err
}

func (c *Consumer) Run(ctx context.Context) {
	nextClaim := time.Now()
	cursor := "0-0"
	for ctx.Err() == nil {
		if err := c.ensure(ctx); err != nil {
			c.wait(ctx, err)
			continue
		}
		if !time.Now().Before(nextClaim) {
			messages, next, err := c.Redis.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: c.Stream, Group: c.Group, Consumer: c.Name, MinIdle: 30 * time.Second, Start: cursor, Count: 20}).Result()
			if err != nil && err != redis.Nil {
				c.wait(ctx, err)
			} else {
				cursor = next
				for _, m := range messages {
					c.process(ctx, m)
				}
			}
			nextClaim = time.Now().Add(5 * time.Second)
		}
		streams, err := c.Redis.XReadGroup(ctx, &redis.XReadGroupArgs{Group: c.Group, Consumer: c.Name, Streams: []string{c.Stream, ">"}, Count: 20, Block: time.Second}).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			c.wait(ctx, err)
			continue
		}
		for _, s := range streams {
			for _, m := range s.Messages {
				if ctx.Err() != nil {
					return
				}
				c.process(ctx, m)
			}
		}
	}
}

func (c *Consumer) process(ctx context.Context, msg redis.XMessage) {
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var err error
	if c.Incidents {
		raw, ok := msg.Values["payload"].(string)
		var incident monitor.Incident
		if !ok {
			err = fmt.Errorf("missing incident payload")
		} else {
			err = json.Unmarshal([]byte(raw), &incident)
		}
		if err == nil && (incident.ID == "" || incident.DeviceID == "" || incident.ObservedAt.IsZero()) {
			err = fmt.Errorf("invalid incident identity")
		}
		if err != nil {
			c.reject(work, msg, err)
			return
		}
		err = c.Store.SaveIncident(work, incident, c.AIEnabled)
	} else {
		metric, decodeErr := monitor.Decode(msg)
		if decodeErr != nil {
			c.reject(work, msg, decodeErr)
			return
		}
		err = c.Store.SaveTelemetry(work, c.Stream, msg.ID, metric)
	}
	if err != nil {
		log.Printf("storage failed stream=%s event=%s left pending: %v", c.Stream, msg.ID, err)
		return
	}
	// PostgreSQL has committed before acknowledgment. Replay after a crash is
	// harmless because both inserts use unique IDs and ON CONFLICT DO NOTHING.
	if err := c.Redis.XAck(work, c.Stream, c.Group, msg.ID).Err(); err != nil {
		log.Printf("storage acknowledgment: %v", err)
	}
}

var rejectScript = redis.NewScript(`
local dedup=KEYS[1]..':rejected:'..ARGV[1]
local kind=redis.call('TYPE',KEYS[2]).ok
if kind~='none' and kind~='stream' then return redis.error_reply('WRONGTYPE dead-letter stream') end
if not redis.call('HEXISTS',dedup,ARGV[2]) then
 redis.call('XADD',KEYS[2],'*','source_event_id',ARGV[2],'payload',ARGV[3],'error',ARGV[4])
 redis.call('HSET',dedup,ARGV[2],1)
end
return redis.call('XACK',KEYS[1],ARGV[1],ARGV[2])
`)

func (c *Consumer) reject(ctx context.Context, msg redis.XMessage, reason error) {
	body, _ := json.Marshal(msg.Values)
	if err := rejectScript.Run(ctx, c.Redis, []string{c.Stream, c.Stream + ":storage-dead"}, c.Group, msg.ID, string(body), reason.Error()).Err(); err != nil {
		log.Printf("storage reject left pending: %v", err)
	}
}

func (c *Consumer) wait(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	log.Printf("storage stream=%s retrying: %v", c.Stream, err)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
