FROM golang:1.27.4-alpine3.24 AS supervisor-builder

WORKDIR /src

COPY go.mod ./
COPY internal/supervisor ./internal/supervisor
COPY cmd/judge-supervisor ./cmd/judge-supervisor

RUN CGO_ENABLED=0 go build -o /judge-supervisor ./cmd/judge-supervisor

FROM gcc:15.3.0-trixie

RUN groupadd --gid 10001 runner \
    && useradd --uid 10001 --gid runner runner

COPY --from=supervisor-builder /judge-supervisor /usr/local/bin/judge-supervisor

WORKDIR /workspace

USER runner

CMD ["/output/program"]
