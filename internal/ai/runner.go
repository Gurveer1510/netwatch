package ai

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/Gurveer1510/netwatch/internal/store"
)

func Run(ctx context.Context, s *store.Store, client *Client) {
	for ctx.Err() == nil {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		incident, attempt, err := s.ClaimAnalysis(queryCtx)
		cancel()
		if err != nil {
			if !store.IsEmpty(err) && ctx.Err() == nil {
				log.Printf("claim AI analysis: %v", err)
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		analysisCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		history, err := s.HistoryAt(analysisCtx, incident.DeviceID, 12, incident.ObservedAt)
		var analysis Analysis
		if err == nil {
			analysis, err = client.Analyze(analysisCtx, incident, history)
		}
		cancel()
		if ctx.Err() != nil {
			return
		} // Lease permits recovery after shutdown.
		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err != nil {
			log.Printf("AI incident=%s attempt=%d: %v", incident.ID, attempt, err)
			if saveErr := s.FailAnalysis(saveCtx, incident.ID, attempt, err.Error()); saveErr != nil {
				log.Printf("save AI failure: %v", saveErr)
			}
		} else {
			body, _ := json.Marshal(analysis)
			if err := s.CompleteAnalysis(saveCtx, incident.ID, attempt, body); err != nil {
				log.Printf("save AI analysis: %v", err)
			}
		}
		cancel()
	}
}
