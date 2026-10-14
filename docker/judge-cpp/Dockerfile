FROM golang:1.24-alpine AS supervisor-builder
WORKDIR /src
COPY go.mod ./
COPY internal/supervisor ./internal/supervisor
COPY cmd/judge-supervisor ./cmd/judge-supervisor
RUN CGO_ENABLED=0 go build -o /judge-supervisor ./cmd/judge-supervisor

FROM gcc:14-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends nlohmann-json3-dev \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 runner && useradd --uid 10001 --gid runner runner
COPY --from=supervisor-builder /judge-supervisor /usr/local/bin/judge-supervisor
WORKDIR /workspace
USER runner
CMD ["/output/program"]
