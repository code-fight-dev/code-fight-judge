FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/judge ./cmd/judge

FROM alpine:3.20

WORKDIR /app

RUN adduser -D -u 10001 appuser

COPY --from=builder /bin/judge /app/judge

USER appuser

EXPOSE 8081

CMD ["/app/judge"]