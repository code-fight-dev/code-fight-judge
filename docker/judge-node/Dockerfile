FROM golang:1.27.3-alpine3.24 AS supervisor-builder
WORKDIR /src
COPY go.mod ./
COPY internal/supervisor ./internal/supervisor
COPY cmd/judge-supervisor ./cmd/judge-supervisor
RUN CGO_ENABLED=0 go build -o /judge-supervisor ./cmd/judge-supervisor

FROM node:24-bookworm-slim
WORKDIR /opt/judge-node
RUN npm install --omit=dev --ignore-scripts --no-audit --no-fund --save-exact typescript@5.9.3 @types/node@24.3.0 \
    && npm cache clean --force \
    && groupadd --gid 10001 runner && useradd --uid 10001 --gid runner runner
COPY docker/judge-node/compile.cjs docker/judge-node/run.cjs ./
COPY --from=supervisor-builder /judge-supervisor /usr/local/bin/judge-supervisor
WORKDIR /workspace
USER runner
CMD ["node", "/opt/judge-node/run.cjs", "/output/solution.cjs"]
