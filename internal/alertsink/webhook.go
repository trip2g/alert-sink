package alertsink

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// WebhookPayload is the Alertmanager webhook message, version 4.
// https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
type WebhookPayload struct {
	Version  string  `json:"version"`
	GroupKey string  `json:"groupKey"`
	Status   string  `json:"status"`
	Receiver string  `json:"receiver"`
	Alerts   []Alert `json:"alerts"`
}

// Alert is one alert inside a webhook delivery.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Name returns the alertname label, or "unknown" when absent.
func (a Alert) Name() string {
	if n := a.Labels["alertname"]; n != "" {
		return n
	}
	return "unknown"
}

// Severity returns the severity label, or "none" when absent.
func (a Alert) Severity() string {
	if s := a.Labels["severity"]; s != "" {
		return s
	}
	return "none"
}

// Resolved reports whether this alert event is a resolution.
func (a Alert) Resolved() bool {
	return a.Status == "resolved"
}

const maxWebhookBody = 4 << 20

// ParseWebhook decodes and validates an Alertmanager webhook body.
func ParseWebhook(r io.Reader) (*WebhookPayload, error) {
	var p WebhookPayload
	dec := json.NewDecoder(io.LimitReader(r, maxWebhookBody))
	err := dec.Decode(&p)
	if err != nil {
		return nil, fmt.Errorf("decode webhook: %w", err)
	}
	if p.Version != "4" {
		return nil, fmt.Errorf("unsupported webhook version %q (want 4)", p.Version)
	}
	for i, a := range p.Alerts {
		if a.Fingerprint == "" {
			return nil, fmt.Errorf("alert %d has no fingerprint", i)
		}
		if a.StartsAt.IsZero() {
			return nil, fmt.Errorf("alert %d has no startsAt", i)
		}
	}
	return &p, nil
}
