// Command alert-sink receives Alertmanager webhook deliveries and writes
// each alert as an incident note into a trip2g instance. See the README and
// internal/alertsink for the note schema and the self-mint auth model.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trip2g/alert-sink/internal/alertsink"
)

func main() {
	cfg, err := alertsink.LoadConfig()
	if err != nil {
		log.Fatalf("alert-sink: %v", err)
	}

	var client *alertsink.Trip2gClient
	if cfg.WriteEnabled() {
		client = alertsink.NewTrip2gClient(cfg.Trip2gURL, cfg.JwtSecret, cfg.Email, cfg.Timeout)
		log.Printf("alert-sink: writing incidents to %s", cfg.Trip2gURL)
	} else {
		log.Printf("alert-sink: ALERT_SINK_TRIP2G_URL or ALERT_SINK_JWT_SECRET not set, trip2g writes disabled")
	}

	metrics := &alertsink.Metrics{}
	sink := alertsink.NewSink(cfg, client, metrics)
	sink.Start()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           alertsink.NewServeMux(sink, metrics),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("alert-sink: listening on %s", cfg.ListenAddr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("alert-sink: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	sink.Shutdown()
}
