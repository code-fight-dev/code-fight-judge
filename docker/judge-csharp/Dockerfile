FROM golang:1.27.4-alpine3.24 AS supervisor-builder
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/supervisor ./internal/supervisor
COPY cmd/judge-supervisor ./cmd/judge-supervisor
RUN CGO_ENABLED=0 go build -o /judge-supervisor ./cmd/judge-supervisor

FROM mcr.microsoft.com/dotnet/sdk:10.0-alpine
ENV DOTNET_PROCESSOR_COUNT=1
RUN addgroup -g 10001 runner && adduser -D -u 10001 -G runner runner
COPY --from=supervisor-builder /judge-supervisor /usr/local/bin/judge-supervisor
COPY docker/judge-csharp/compile.sh docker/judge-csharp/Submission.runtimeconfig.json /opt/judge-csharp/
RUN sed -i 's/\r$//' /opt/judge-csharp/compile.sh \
    && chmod 0555 /opt/judge-csharp/compile.sh \
    && printf '%s\n' /usr/share/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net10.0/*.dll \
       | sed 's/^/-reference:/' > /opt/judge-csharp/references.rsp
WORKDIR /workspace
USER runner
CMD ["dotnet", "exec", "--runtimeconfig", "/opt/judge-csharp/Submission.runtimeconfig.json", "/output/Submission.dll"]
