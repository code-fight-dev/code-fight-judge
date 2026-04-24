FROM golang:1.24-alpine

WORKDIR /workspace

RUN adduser -D -u 10001 runner

USER runner

CMD ["sh", "-c", "go run main.go"]