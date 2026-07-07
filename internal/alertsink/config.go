package alertsink

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from the environment. When Trip2gURL or the JWT secret is
// missing the sink is feature-inert: it still serves /webhook, /healthz and
// /metrics, but skips the trip2g write and only logs.
type Config struct {
	ListenAddr   string        // ALERT_SINK_LISTEN_ADDR, default 127.0.0.1:9095
	Trip2gURL    string        // ALERT_SINK_TRIP2G_URL, e.g. http://trip2g:8080
	JwtSecret    string        // ALERT_SINK_JWT_SECRET or ALERT_SINK_JWT_SECRET_FILE
	Email        string        // ALERT_SINK_EMAIL, HAT identity, default alert-sink@local
	TelegramTags []string      // ALERT_SINK_TELEGRAM_TAGS, comma-separated, default "incidents"; "none" disables
	QueueSize    int           // ALERT_SINK_QUEUE_SIZE, default 1000
	Timeout      time.Duration // ALERT_SINK_TRIP2G_TIMEOUT, default 15s
	AutoRelease  bool          // ALERT_SINK_AUTO_RELEASE, default true
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// LoadConfig reads the sink configuration from the environment.
func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr: envOr("ALERT_SINK_LISTEN_ADDR", "127.0.0.1:9095"),
		Trip2gURL:  envOr("ALERT_SINK_TRIP2G_URL", ""),
		JwtSecret:  envOr("ALERT_SINK_JWT_SECRET", ""),
		Email:      envOr("ALERT_SINK_EMAIL", "alert-sink@local"),
	}

	if cfg.JwtSecret == "" {
		if path := envOr("ALERT_SINK_JWT_SECRET_FILE", ""); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return Config{}, fmt.Errorf("read ALERT_SINK_JWT_SECRET_FILE: %w", err)
			}
			cfg.JwtSecret = strings.TrimSpace(string(data))
		}
	}

	tags := envOr("ALERT_SINK_TELEGRAM_TAGS", "incidents")
	if tags != "none" {
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				cfg.TelegramTags = append(cfg.TelegramTags, t)
			}
		}
	}

	size := envOr("ALERT_SINK_QUEUE_SIZE", "1000")
	n, err := strconv.Atoi(size)
	if err != nil || n <= 0 {
		return Config{}, fmt.Errorf("invalid ALERT_SINK_QUEUE_SIZE %q", size)
	}
	cfg.QueueSize = n

	timeout := envOr("ALERT_SINK_TRIP2G_TIMEOUT", "15s")
	d, err := time.ParseDuration(timeout)
	if err != nil || d <= 0 {
		return Config{}, fmt.Errorf("invalid ALERT_SINK_TRIP2G_TIMEOUT %q", timeout)
	}
	cfg.Timeout = d

	switch v := envOr("ALERT_SINK_AUTO_RELEASE", "true"); v {
	case "true":
		cfg.AutoRelease = true
	case "false":
		cfg.AutoRelease = false
	default:
		return Config{}, fmt.Errorf("invalid ALERT_SINK_AUTO_RELEASE %q (want true or false)", v)
	}

	return cfg, nil
}

// WriteEnabled reports whether the trip2g write path is configured.
func (c Config) WriteEnabled() bool {
	return c.Trip2gURL != "" && c.JwtSecret != ""
}
