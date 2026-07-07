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
  label set and timing in structured frontmatter
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
4. For a resolved alert the sink patches exactly two adjacent frontmatter
   lines, `status` and `ends_at`, via a single find and replace. It never
   rewrites the whole note, so anything a human or agent added to the note in
   the meantime survives. If the patch target is missing because the firing
   event was lost, the sink creates the note directly in its resolved state.
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
status: firing
ends_at: null
starts_at: "2026-07-07T14:37:00Z"
fingerprint: "ab12cd34ef56"
labels:
  instance: "node-02:9100"
created_at: "2026-07-07T14:37:00Z"
telegram_publish_at: "2026-07-07T14:37:00Z"
telegram_publish_tags:
  - "incidents"
---
**NodeDown** firing (severity: critical).

Node exporter target is down.

Postmortem: [[incidents/2026/07/ab12cd34ef56-postmortem]]
```

### Postmortems and ownership

Two note types share the `incidents/` folder and never collide:

| Note | Path | Written by |
|---|---|---|
| Incident | `incidents/YYYY/MM/<ts>-<alertname>-<fp>.md` | alert-sink only |
| Postmortem | `incidents/YYYY/MM/<fp>-postmortem.md` | human or agent only |

Every incident note carries a wikilink to its postmortem path. The sink never
writes that path, and its resolution patch never touches anything but the two
status lines, so postmortem work is safe from the machine.

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
