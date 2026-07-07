package alertsink

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
)

// Metrics holds the sink's Prometheus counters. Rendered by hand in the text
// exposition format to keep the dependency set at stdlib + golang-jwt.
type Metrics struct {
	Received atomic.Int64
	Written  atomic.Int64
	Errors   atomic.Int64
	Dropped  atomic.Int64
	Skipped  atomic.Int64
}

// NewServeMux wires the sink's HTTP surface: POST /webhook (Alertmanager),
// GET /healthz, GET /metrics.
func NewServeMux(sink *Sink, metrics *Metrics) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		payload, err := ParseWebhook(r.Body)
		if err != nil {
			log.Printf("alert-sink: bad webhook: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Enqueue and acknowledge immediately; the trip2g write happens on
		// the worker so Alertmanager never waits on trip2g.
		sink.Enqueue(payload.Alerts)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writeCounter(w, "alert_sink_received_total", "Alert events received from Alertmanager.", metrics.Received.Load())
		writeCounter(w, "alert_sink_written_total", "Alert events written to trip2g.", metrics.Written.Load())
		writeCounter(w, "alert_sink_errors_total", "Failed trip2g write attempts (retried).", metrics.Errors.Load())
		writeCounter(w, "alert_sink_dropped_total", "Alert events dropped on queue overflow.", metrics.Dropped.Load())
		writeCounter(w, "alert_sink_skipped_total", "Alert events skipped because the trip2g write path is not configured.", metrics.Skipped.Load())
		fmt.Fprintf(w, "# HELP alert_sink_queue_length Alert events waiting for the write worker.\n")
		fmt.Fprintf(w, "# TYPE alert_sink_queue_length gauge\n")
		fmt.Fprintf(w, "alert_sink_queue_length %d\n", len(sink.queue))
	})

	return mux
}

func writeCounter(w http.ResponseWriter, name, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s counter\n", name)
	fmt.Fprintf(w, "%s %d\n", name, v)
}
