# Code Fight Judge

The code execution service for [Code Fight](https://github.com/code-fight-dev).
It accepts submissions from the backend, compiles and runs them against test
cases in isolated containers, and reports verdicts, scores, execution time,
and memory usage through authenticated callbacks.

The service is written in Go and uses PostgreSQL for its persistent job queue,
Docker with gVisor (`runsc`) for execution, and Prometheus for metrics.

## How it works

1. The backend submits source code, a language/version pair, resource limits,
   and test cases to the judge API.
2. The judge validates the request and stores a queued job in PostgreSQL.
3. A worker claims the job, prepares the language adapter, and compiles the
   submission when required.
4. Each test runs in a separate sandbox. The runner checks its output and
   collects the result and resource measurements.
5. The judge delivers running and completion callbacks to the backend.

Worker leases and heartbeats allow interrupted jobs to be reclaimed. Callbacks
can be repeated during recovery; consumers must handle them idempotently and
reject older attempts using `started_at`.
If final callback delivery fails, the job stays recoverable and is executed
again after its lease expires. It becomes terminal only after delivery succeeds.

## Supported languages

| Language | API identifier | API version | Runtime image |
| --- | --- | --- | --- |
| Go | `go` | `1.24` | `judge-go:1.27` |
| Python | `python` | `3.12` | `judge-python:3.12` |
| C++ | `cpp` | `20` | `judge-cpp:20` |
| JavaScript | `javascript` | `node24` | `judge-node:24` |
| TypeScript | `typescript` | `5.9.3` | `judge-node:24` |

The language registry is the source of truth for accepted language/version
pairs. JavaScript and TypeScript share a Node.js 24 image. The C++ image includes
nlohmann/json; the Node image includes TypeScript and Node type definitions.
Submissions use the dependencies supplied by their runtime image.
The Go API version remains `1.24` for backend compatibility; its runtime image
uses the maintained Go 1.27 toolchain.

## Submission contract

Go, C++, JavaScript, and TypeScript support a `Solve` or `solve` function.
Arguments are consecutive JSON values in parameter order; string results are
printed as text and other supported results are JSON encoded. For example:

```typescript
export function Solve(values: number[], extra: number): number {
  return values[0] + extra;
}
```

With input `[3,4]\n5`, this function produces `8`. JSON values may span multiple
lines. A single string parameter can receive raw text; JavaScript can declare
it with JSDoc (`@param {string} input`) or use `solve(input)`.

- **Go:** typed functions return one value. A sole top-level function can also
  serve as the entry point. Full programs with `main` manage stdin/stdout directly.
- **Python:** define `solve(input_data)`; it receives stdin as a string. The
  adapter prints a non-`None` result using `str`.
- **C++:** functions accept standard JSON-compatible types, including nested
  vectors, strings, booleans, and numeric types. Full programs with `main` are
  supported. Compilation uses C++20 and `-O2`.
- **JavaScript / TypeScript:** function declarations, `const` function expressions,
  named exports, and async results are supported. Parameters must be required,
  without defaults or rest parameters. TypeScript is checked in strict mode and
  compiled to CommonJS targeting ES2022.

Node adapters reject unsafe JSON integers, non-finite numbers, undefined
results, and BigInt results. Large scalar integer results can be returned as
decimal strings. Adapters and starter templates must agree on input types.

Output checking normalizes line endings and trims surrounding whitespace.
JSON output is also compared structurally: object key order is irrelevant,
array order matters, and decimal numbers are compared exactly without float64
rounding. There is no floating-point tolerance. Tests with `skip_output_check`
return execution results without comparing them to expected output.

## API

The HTTP server listens on port `8081` by default.

| Endpoint | Purpose | Authentication |
| --- | --- | --- |
| `POST /submissions` | Queue a submission; returns `202` with its judge job ID | `X-Judge-Token` |
| `GET /languages` | List supported language/version pairs | `X-Judge-Token` |
| `GET /healthz` | Service liveness | None |
| `GET /readyz` | PostgreSQL, Docker, and runtime image readiness | None |
| `GET /metrics` | Prometheus metrics | None |

Request and job types are defined in [internal/jobs/models.go](internal/jobs/models.go).
Callback payloads are defined in [internal/callback/client.go](internal/callback/client.go).
The callback destination and credentials come from service configuration.
Readiness probes share a cached result for five seconds, including failures,
so concurrent requests cannot start an unbounded number of Docker checks.

## Local development

Prerequisites: Go 1.27.3 or later, PostgreSQL, Docker, and the `runsc` runtime.
The PostgreSQL database must contain the `judge_jobs` table; its schema is
managed by the [backend migrations](https://github.com/code-fight-dev/code-fight-backend/tree/main/internal/database/migrations).

Build runtime images from the repository root:

```sh
docker build -f docker/judge-go/Dockerfile -t judge-go:1.27 .
docker build -f docker/judge-python/Dockerfile -t judge-python:3.12 .
docker build -f docker/judge-cpp/Dockerfile -t judge-cpp:20 .
docker build -f docker/judge-node/Dockerfile -t judge-node:24 .
```

Copy `.env.example` to `.env` and set the database URL, callback URL, and two
independent random tokens. The application reads environment variables; it
does not automatically load `.env`.

```sh
set -a
. ./.env
set +a
go run ./cmd/judge
```

| Setting | Purpose |
| --- | --- |
| `DATABASE_URL` | PostgreSQL connection string |
| `JUDGE_API_TOKEN` | Authentication for backend requests |
| `JUDGE_CALLBACK_URL`, `JUDGE_CALLBACK_TOKEN` | Backend callback endpoint and authentication |
| `JUDGE_WORKDIR` | Directory for submission workspaces |
| `JUDGE_GO_IMAGE`, `JUDGE_PYTHON_IMAGE`, `JUDGE_CPP_IMAGE`, `JUDGE_NODE_IMAGE` | Runtime image references |
| `JUDGE_RUNTIME` | Docker runtime; defaults to `runsc` |
| `JUDGE_COMPILE_TIMEOUT` | Independent compiler wall-time limit; defaults to `60s` |
| `JUDGE_CPU_COUNT`, `JUDGE_DEFAULT_PIDS`, `JUDGE_DEFAULT_TMPFS_MB` | Sandbox resource settings |
| `JUDGE_MAX_*` | Upper bounds on submission limits and test payloads |

See [.env.example](.env.example) for common settings and
[internal/config/config.go](internal/config/config.go) for all defaults and validation.

## Execution and resource accounting

A trusted supervisor enforces execution limits and runs submitted code as
UID/GID `10001` without capabilities. Sandboxes have no network access, use a
read-only root filesystem, disable swap, and terminate remaining descendants
after execution. Combined stdout/stderr is bounded by `output_kb`; compiler
artifacts have a separate directory size limit.

`time_ms` limits wall time per test, including runtime startup and input
decoding but excluding container startup. `run_time_ms` sums executed tests;
compilation time is reported separately and bounded by `JUDGE_COMPILE_TIMEOUT`.
Timeout and OOM verdicts use trusted execution results rather than interpreting
process exit codes alone.

`memory_kb` reports the lifetime cgroup peak for the whole sandbox, including
the runtime, supervisor, descendants, tmpfs, and sandbox overhead. Missing
measurements are `null`; sampled or current usage is not substituted for a peak.
The submission peak is `null` if any executed test lacks a measurement.

When running the judge in Docker, provide Docker daemon access and share
`JUDGE_WORKDIR` with the Docker host at the same absolute path. Mount the host's
`/sys/fs/cgroup` at `/host/sys/fs/cgroup:ro` for memory accounting; a custom cgroup
parent also requires host `/proc` at `/host/proc:ro`. These mounts are for the
judge service. Production configuration requires `runsc`, HTTPS callbacks,
explicit tokens, and runtime image references pinned by digest.

## Testing

```sh
go test ./...
go test -race ./...
go vet ./...
```

Container and PostgreSQL tests are opt-in:

```sh
JUDGE_SANDBOX_INTEGRATION=1 go test -tags integration ./internal/runner
JUDGE_TEST_DATABASE_URL="$DATABASE_URL" go test -tags integration ./internal/storage
```

Container tests use `runsc` by default; `JUDGE_INTEGRATION_RUNTIME` selects a
different installed runtime. Local adapter fixtures are enabled with
`JUDGE_LOCAL_RUNTIME_TESTS=1` and require g++, nlohmann/json headers, Node,
TypeScript, and Node type definitions. CI builds all runtime images and checks
adapters, sandbox limits and isolation under `runsc`, and job recovery against
PostgreSQL.

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/judge` | Service entry point |
| `cmd/judge-supervisor` | Trusted sandbox supervisor |
| `internal/http`, `internal/config`, `internal/app` | API, configuration, and service wiring |
| `internal/languages`, `internal/runner` | Runtime registry, adapters, and result checking |
| `internal/sandbox`, `internal/supervisor` | Container execution and supervisor protocol |
| `internal/queue`, `internal/storage`, `internal/jobs` | Persistent queue and job lifecycle |
| `internal/callback`, `internal/metrics` | Backend delivery and observability |
| `docker` | Language runtime images |
