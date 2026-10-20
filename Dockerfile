FROM golang:1.27.3-alpine3.24 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/judge ./cmd/judge

FROM alpine:3.24

WORKDIR /app

RUN apk add --no-cache docker-cli

COPY --from=builder /bin/judge /app/judge

EXPOSE 8081

CMD ["/app/judge"]