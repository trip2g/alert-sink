# alert-sink

A small Go service that turns Alertmanager webhooks into incident notes in a
[trip2g](https://github.com/trip2g/trip2g) knowledge base. Every alert becomes
a markdown note with frontmatter; the notes aggregate into an incident
magazine, and (optionally) each incident posts to a Telegram channel and gets
edited in place when it resolves.

Dependencies: Go stdlib plus `golang-jwt`. One binary, no database.

## Why put alerts in a knowledge base

Alertmanager shows you what is firing right now. History is the gap: once an
alert resolves, it only survives as a Prometheus time series (bounded by
retention) or as a message that scrolled by in a chat.

Landing each incident as a markdown note gives you:

- a permanent, searchable incident log, one note per incident, with the full
  label set and timing in structured frontmatter. Full-text (BM25) search works
  out of the box; trip2g's semantic vector search is opt-in and off in this demo
  (it needs your own embedding server), see the trip2g search docs
- a magazine page that lists all incidents newest first, for humans
- a place for postmortems: each incident note links to a sibling postmortem
  note that a human (or an agent) writes later, and the sink is careful to
  never touch it
- notes are plain markdown over an API, so anything that can read the
  knowledge base can reason over your incident history

## Quickstart

```bash
cd demo
docker compose up --build
```

Wait a minute, then open:

- http://localhost:8080/incidents for the incident magazine (the demo alert
  `DemoAlwaysFiring` shows up first)
- http://localhost:9090/alerts for Prometheus, http://localhost:9093 for
  Alertmanager

To watch a full firing to resolved lifecycle:

```bash
docker compose stop demo-target    # DemoTargetDown fires after ~1 minute
docker compose start demo-target   # the same incident note flips to resolved
```

The incident note is updated in place: only its `status` and `ends_at` fields
change, everything else (including any edits you made to the note) survives.

## How it works

```
Prometheus ──alerts──> Alertmanager ──webhook──> alert-sink ──updateNotes──> trip2g
                                                    │                          │
                                                    └── /metrics               ├── /incidents magazine
                                                                               └── Telegram post (optional)
```

1. Alertmanager POSTs its v4 webhook payload to `POST /webhook`. The sink
   validates, enqueues, and answers 200 within milliseconds. Alertmanager
   never waits on trip2g, and other receivers (like a native Telegram
   receiver) are independent by Alertmanager's design.
2. A worker goroutine writes each alert to trip2g, retrying with exponential
   backoff (5s doubling to 5m) while trip2g is down. The queue is bounded;
   on overflow the oldest events are dropped and counted in
   `alert_sink_dropped_total`.
3. For a firing alert the sink upserts a note at
   `incidents/YYYY/MM/<startsAt-unix>-<alertname>-<fingerprint>.md`. The path
   is a pure function of the alert identity, so re-deliveries and retries hit
   the same note. The upsert is create-only: if the note already exists (a
   repeat notification for a still-firing alert), it is left untouched.
4. For a resolved alert the sink patches a single contiguous block that spans
   the last two frontmatter lines (`status` and `ends_at`), the closing `---`,
   and the visible status callout immediately after it. A single find-and-replace
   flips the note from firing to resolved in place: the frontmatter fields change
   and the callout emoji flips from 🔴 to ✅. Anything after that block, including
   the postmortem wikilink and any other content, is never touched. If the patch
   target is missing because the firing event was lost, the sink creates the note
   directly in its resolved state.
5. On first write the sink also creates `incidents/index.md` (once, never
   overwritten): a trip2g magazine note that lists every incident newest
   first, excluding postmortems.

### Incident note shape

```markdown
---
title: "NodeDown - 2026-07-07 14:37"
free: true
alertname: "NodeDown"
severity: "critical"
starts_at: "2026-07-07T14:37:00Z"
fingerprint: "ab12cd34ef56"
labels:
  instance: "node-02:9100"
created_at: "2026-07-07T14:37:00Z"
telegram_publish_at: "2026-07-07T14:37:00Z"
telegram_publish_tags:
  - "incidents"
status: firing
ends_at: null
---

> 🔴 **FIRING** · 2026-07-07 14:37 UTC
> **NodeDown** · critical · Node exporter target is down.

Prometheus cannot scrape node-02.

Postmortem: [[incidents/2026/07/ab12cd34ef56-postmortem]]
```

On resolve, the `status`/`ends_at` frontmatter lines and the callout line all
flip together in a single patch:

```markdown
status: resolved
ends_at: "2026-07-07T14:41:00Z"
---

> ✅ **RESOLVED** · 2026-07-07 14:41 UTC
```

### Incident note ownership

The incident note is **machine-owned**: alert-sink writes and patches it
entirely. Humans never edit it.

The **postmortem** is a **separate, human-owned file** at
`incidents/YYYY/MM/<fingerprint>-postmortem.md`. It does not exist by default;
the unresolved wikilink in the incident note acts as a "create" affordance in
Obsidian and trip2g. The sink never writes or reads that path.

Two note types share the `incidents/` folder and never collide:

| Note | Path | Written by |
|---|---|---|
| Incident | `incidents/YYYY/MM/<ts>-<alertname>-<fp>.md` | alert-sink only |
| Postmortem | `incidents/YYYY/MM/<fp>-postmortem.md` | human or agent only |

The value of the split is clean separation: the incident note carries machine
facts (timing, labels, status), and the postmortem carries human analysis.
They are cross-linked by wikilink. The sink's patch only ever touches the
contiguous status block at the bottom of the frontmatter plus the callout line
immediately below it; everything else in the note is untouched.

### Telegram posts

trip2g publishes any note that carries `telegram_publish_at` and
`telegram_publish_tags` to the Telegram chats subscribed to those tags, and
edits the message in place when the note changes. So one incident becomes one
Telegram message that flips from firing to resolved on its own.

The sink writes both fields into every incident note (tag `incidents` by
default). To get actual messages, wire the trip2g side once: add a bot to the
instance, link a chat, and subscribe it to the tag. Without that wiring the
fields are inert and nothing is sent.

## Configuration

All configuration is via environment variables. If `ALERT_SINK_TRIP2G_URL` or
the JWT secret is unset, the sink runs but skips trip2g writes entirely (it
still serves `/webhook`, `/healthz`, `/metrics`), so it is safe to deploy
dark.

| Variable | Default | Meaning |
|---|---|---|
| `ALERT_SINK_LISTEN_ADDR` | `127.0.0.1:9095` | HTTP listen address |
| `ALERT_SINK_TRIP2G_URL` | (unset) | Base URL of the trip2g instance to write to |
| `ALERT_SINK_JWT_SECRET` | (unset) | The trip2g instance's JWT secret, used to self-sign write tokens |
| `ALERT_SINK_JWT_SECRET_FILE` | (unset) | Read the secret from a file instead |
| `ALERT_SINK_EMAIL` | `alert-sink@local` | Identity the sink writes as |
| `ALERT_SINK_TELEGRAM_TAGS` | `incidents` | Comma-separated publish tags; `none` disables the Telegram fields |
| `ALERT_SINK_QUEUE_SIZE` | `1000` | Bounded write queue capacity |
| `ALERT_SINK_TRIP2G_TIMEOUT` | `15s` | Per-request timeout to trip2g |

Endpoints: `POST /webhook` (Alertmanager v4), `GET /healthz`, `GET /metrics`
(Prometheus text format: received, written, errors, dropped, skipped, queue
length).

Alertmanager side:

```yaml
receivers:
  - name: default
    webhook_configs:
      - url: http://127.0.0.1:9095/webhook
        send_resolved: true
```

## Auth model: self-mint

trip2g has no long-lived scoped write tokens. Its admin write path is: sign a
short-lived HAT JWT (HS256, claims `e` for email and `ae` for admin enter)
with the instance's `JWT_SECRET`, exchange it at `POST /_system/hat` for a
session, then call the `updateNotes` GraphQL mutation at `/_system/graphql`
with that session as a Bearer token.

alert-sink holds the instance's JWT secret and self-signs a fresh 5 minute
HAT for every write. No third party mints tokens for it and no credential
with a longer lifetime than 5 minutes ever exists outside the secret itself.

The honest caveat: a HAT session is admin on its instance. There is no
path-level scoping inside trip2g, so least privilege comes from blast radius.
Point the sink at a dedicated trip2g instance that holds only alert history,
and the secret, even fully compromised, reaches nothing else.

## Development

```bash
go build ./...
go test ./...
```

The test suite covers the webhook to note transform (firing and resolved),
path and frontmatter generation, the anti-clobber patch semantics against a
fake trip2g, and HAT signing.

## License

MIT
