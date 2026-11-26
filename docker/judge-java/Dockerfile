FROM golang:1.27.4-alpine3.24 AS supervisor-builder
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/supervisor ./internal/supervisor
COPY cmd/judge-supervisor ./cmd/judge-supervisor
RUN CGO_ENABLED=0 go build -o /judge-supervisor ./cmd/judge-supervisor

FROM eclipse-temurin:21-jdk-alpine-3.22
RUN addgroup -g 10001 runner && adduser -D -u 10001 -G runner runner
COPY --from=supervisor-builder /judge-supervisor /usr/local/bin/judge-supervisor
WORKDIR /workspace
USER runner
CMD ["java", "-cp", "/output", "Main"]
