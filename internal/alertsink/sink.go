package alertsink

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// Sink accepts alert events and writes incident notes into trip2g on a
// worker goroutine. The webhook handler only enqueues; trip2g being down or
// slow never blocks or fails the Alertmanager delivery path.
type Sink struct {
	cfg     Config
	client  *Trip2gClient
	queue   chan Alert
	metrics *Metrics

	indexMu   sync.Mutex
	indexDone bool

	wg   sync.WaitGroup
	stop context.CancelFunc
}

// NewSink builds a sink. client may be nil when the write path is not
// configured (feature-inert mode).
func NewSink(cfg Config, client *Trip2gClient, metrics *Metrics) *Sink {
	return &Sink{
		cfg:     cfg,
		client:  client,
		queue:   make(chan Alert, cfg.QueueSize),
		metrics: metrics,
	}
}

// Enqueue adds alerts from one webhook delivery to the write queue. On
// overflow the event is dropped and counted; the caller still returns 200 to
// Alertmanager (its own retry cadence re-delivers firing groups).
func (s *Sink) Enqueue(alerts []Alert) {
	for _, a := range alerts {
		s.metrics.Received.Add(1)
		if s.client == nil {
			log.Printf("alert-sink: trip2g write disabled, skipping %s (%s)", a.Name(), a.Fingerprint)
			s.metrics.Skipped.Add(1)
			continue
		}
		select {
		case s.queue <- a:
		default:
			log.Printf("alert-sink: queue full, dropping %s (%s)", a.Name(), a.Fingerprint)
			s.metrics.Dropped.Add(1)
		}
	}
}

// Start launches the worker goroutine.
func (s *Sink) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Shutdown stops the worker and waits for it to exit.
func (s *Sink) Shutdown() {
	if s.stop != nil {
		s.stop()
	}
	s.wg.Wait()
}

// run drains the queue, retrying each event with exponential backoff so a
// trip2g outage delays writes instead of losing them (up to queue capacity).
func (s *Sink) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-s.queue:
			s.processWithRetry(ctx, a)
		}
	}
}

const (
	backoffStart = 5 * time.Second
	backoffCap   = 5 * time.Minute
)

func (s *Sink) processWithRetry(ctx context.Context, a Alert) {
	backoff := backoffStart
	for {
		err := s.process(ctx, a)
		if err == nil {
			s.metrics.Written.Add(1)
			return
		}
		s.metrics.Errors.Add(1)
		log.Printf("alert-sink: write %s (%s) failed, retrying in %s: %v", a.Name(), a.Fingerprint, backoff, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > backoffCap {
			backoff = backoffCap
		}
	}
}

// process writes one alert event and publishes a release when anything
// changed (trip2g serves the public site from the live release; without a
// release the notes stay drafts).
func (s *Sink) process(ctx context.Context, a Alert) error {
	err := s.ensureIndex(ctx)
	if err != nil {
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	mutated, err := s.writeNote(cctx, a)
	if err != nil {
		return err
	}
	if mutated && s.cfg.AutoRelease {
		err = s.client.CreateRelease(cctx, "alert-sink "+a.Fingerprint+" "+a.Status)
		if err != nil {
			return err
		}
	}
	return nil
}

// writeNote applies one alert event to its incident note and reports whether
// anything actually changed.
//
// Firing: create-only upsert (expectedHash "" asserts the note is absent).
// A HashMismatch means the note already exists - Alertmanager re-sends
// still-firing groups on its repeat interval, and the note may since carry
// agent edits (postmortem link), so an existing note is left untouched.
//
// Resolved: a single find/replace patch flips the adjacent status/ends_at
// frontmatter lines and nothing else, so agent edits survive. When the note
// is missing entirely (the firing event was lost), fall back to creating the
// resolved note wholesale.
func (s *Sink) writeNote(ctx context.Context, a Alert) (bool, error) {
	if !a.Resolved() {
		err := s.upsertCreateOnly(ctx, NotePath(a), RenderNote(a, s.cfg.TelegramTags))
		if errors.Is(err, ErrHashMismatch) {
			// Repeat notification for an incident already on record.
			return false, nil
		}
		return err == nil, err
	}

	patch := []NoteChange{{Patch: &NotePatch{
		Path:    NotePath(a),
		Find:    FiringPatchBlock(a.StartsAt),
		Replace: ResolvedPatchBlock(a.StartsAt, a.EndsAt),
	}}}
	err := s.client.UpdateNotes(ctx, patch)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNoteNotFound):
		// Missed the firing event: record the incident directly as resolved.
		err = s.upsertCreateOnly(ctx, NotePath(a), RenderNote(a, s.cfg.TelegramTags))
		if errors.Is(err, ErrHashMismatch) {
			return false, nil
		}
		return err == nil, err
	case errors.Is(err, ErrPatchNotFound):
		// The note exists but has no firing status block: already resolved
		// (re-delivery) or reshaped by an agent. Both are fine to leave as is.
		log.Printf("alert-sink: resolution patch target not found for %s, leaving note as is", NotePath(a))
		return false, nil
	default:
		return false, err
	}
}

// upsertCreateOnly writes a note only when it does not exist yet.
func (s *Sink) upsertCreateOnly(ctx context.Context, path, content string) error {
	empty := ""
	return s.client.UpdateNotes(ctx, []NoteChange{{Upsert: &NoteUpsert{
		Path:         path,
		Content:      content,
		ExpectedHash: &empty,
	}}})
}

// ensureIndex creates incidents/index.md once. The create-only upsert makes
// this idempotent without a read: an existing index (possibly customized by
// the operator) answers HashMismatch and is never overwritten.
func (s *Sink) ensureIndex(ctx context.Context) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexDone {
		return nil
	}

	cctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	err := s.upsertCreateOnly(cctx, IndexPath, RenderIndexNote())
	if err != nil && !errors.Is(err, ErrHashMismatch) {
		return err
	}
	s.indexDone = true
	return nil
}
