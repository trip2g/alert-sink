# syntax=docker/dockerfile:1
# alert-sink: Alertmanager webhook -> trip2g incident notes.
# Build from the repo root: docker build -f cmd/alert-sink/Dockerfile -t alert-sink .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/alert-sink ./cmd/alert-sink

FROM scratch
COPY --from=build /out/alert-sink /usr/local/bin/alert-sink
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV ALERT_SINK_LISTEN_ADDR=127.0.0.1:9095
ENTRYPOINT ["/usr/local/bin/alert-sink"]
