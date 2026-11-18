FROM golang:1.27.3-alpine3.24 AS supervisor-builder
WORKDIR /src
COPY go.mod ./
COPY internal/supervisor ./internal/supervisor
COPY cmd/judge-supervisor ./cmd/judge-supervisor
RUN CGO_ENABLED=0 go build -o /judge-supervisor ./cmd/judge-supervisor

FROM golang:1.27.3-alpine3.24
COPY --from=supervisor-builder /judge-supervisor /usr/local/bin/judge-supervisor

WORKDIR /workspace

RUN adduser -D -u 10001 runner

USER runner

CMD ["sh", "-c", "go run main.go"]
