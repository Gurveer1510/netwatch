package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Gurveer1510/netwatch/internal/monitor"
)

func TestAnalysisGroundsEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["stream"] != false || body["format"] == nil {
			t.Error("expected structured nonstreaming output")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"done": true, "message": map[string]string{"content": `{"summary":"Cause unconfirmed.","possible_causes":["Traffic load"],"suggested_checks":["Review CPU history"],"missing_information":["Process activity"],"evidence":["fabricated evidence"]}`}})
	}))
	defer server.Close()
	c := Client{URL: server.URL, Model: "test-model", HTTP: server.Client()}
	result, err := c.Analyze(context.Background(), monitor.Incident{DeviceID: "r1", CPUPercent: 95, Threshold: 90, ObservedAt: time.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 2 || result.Evidence[0] == "fabricated evidence" || result.Model != "test-model" {
		t.Fatalf("evidence must come from application data: %+v", result)
	}
}

func TestRejectMalformedAnalysis(t *testing.T) {
	for _, payload := range []string{`{"done":false}`, `{"done":true,"message":{"content":"not-json"}}`, `{"done":true,"message":{"content":"{}"}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) }))
		c := Client{URL: server.URL, Model: "test", HTTP: server.Client()}
		if _, err := c.Analyze(context.Background(), monitor.Incident{}, nil); err == nil {
			t.Errorf("accepted invalid response: %s", payload)
		}
		server.Close()
	}
}
