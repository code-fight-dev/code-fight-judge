# Code Fight Judge

## Execution and deployment

Both runtime images now include a trusted supervisor. Rebuild them along with
the judge, using the repository root as the build context:

```sh
docker build -f docker/judge-go/Dockerfile -t judge-go:1.24 .
docker build -f docker/judge-python/Dockerfile -t judge-python:3.12 .
docker build -t codefight-judge .
```

Configure `JUDGE_API_TOKEN`, `JUDGE_CALLBACK_URL`, and `JUDGE_CALLBACK_TOKEN`
explicitly in every environment. The old development tokens are rejected.
Callback destinations and credentials in submissions are ignored; only the
configured destination is used, and redirects are rejected. An internal backend
address is supported. Production requires HTTPS callbacks, `runsc`, and runtime
images pinned by digest.

The supervisor runs as root PID 1 with only SETUID, SETGID, and KILL capabilities.
It drops to UID/GID 10001 with no effective, permitted, or ambient capabilities
before starting the submitted program. The root supervisor's timer and private
report tmpfs are inaccessible to the submitted program. Docker's log driver is
disabled; stdout and stderr are captured within a combined `output_kb` budget.
Exceeding it produces `output_limit_exceeded`; oversized compiler artifacts
produce `output_directory_limit_exceeded`. The backend must support these verdicts.

`time_ms` limits wall time for each test, including language startup and input
reading, but excluding Docker/runsc startup and Docker CLI work. `run_time_ms`
is the sum across executed tests; compilation is measured separately. Swap is
disabled for both compilation and execution. A supervisor startup failure is a
system error, not a time-limit verdict; exit codes 124/137 alone do not establish
timeout or OOM.

`memory_kb` is the lifetime cgroup peak of the whole sandbox, including the
supervisor, interpreter/runtime, descendants, tmpfs, and sandbox overhead. It
uses `memory.peak` (cgroup v2) or `memory.max_usage_in_bytes` (v1) while the
supervisor keeps the cgroup alive for up to 30 seconds after task completion. Mount the Docker host's `/sys/fs/cgroup` at
`/host/sys/fs/cgroup:ro` when the judge itself runs in Docker. For a custom cgroup
parent, also mount the host's `/proc` at `/host/proc:ro`. These mounts belong to
the judge service, never to submission containers. The Docker host and the judge
must share the work directory at the same absolute path for bind mounts.

If the host peak is unavailable, memory is reported as `null`; sampled/current
memory is never substituted for a peak. The submission-wide peak is also `null`
if any executed test lacks a measurement. After a container-wide OOM kill, peak
and task duration are reported as `null` when unavailable; the OOM verdict still uses Docker's
confirmed OOM state.

Running jobs have a two-minute lease, renewed every 40 seconds. An expired lease
allows another worker to retry the job. The claim's `started_at` fences stale
workers from renewing or completing a newer claim. Callbacks should be
idempotent by submission ID because recovery can repeat an attempt. They should
ignore callbacks with an older `started_at` than the current attempt. No database
schema migration is needed.

Run `go test ./...` and `go vet ./...`. Real container checks are opt-in:

```sh
JUDGE_SANDBOX_INTEGRATION=1 go test -tags integration ./internal/runner
```

CI also checks supervisor isolation, timeout, output floods, detached processes,
and OOM with Docker/runc, plus job lease recovery with PostgreSQL. To verify the
production runtime, run the same container tests with the default `runsc` runtime
on a host where it is installed. PostgreSQL integration tests use an isolated
schema and require `JUDGE_TEST_DATABASE_URL`.

## Go submissions

Submit a function with typed arguments and one return value. The judge reads the
test input, decodes the arguments, calls the function, and prints the result.
No `package main`, `main`, or manual input parsing is needed for function submissions.

```go
func Solve(prices []int) int {
    // Compute the largest gain.
    return 0
}
```

Entry points are selected in this order: `Solve`, `solve`, or the only top-level
function in the submission. If you have helper functions, name the entry point
`Solve` or `solve`.

Arguments use consecutive JSON values, in parameter order. For example,
`func Solve(values []int, threshold int) int` receives:

```text
[1, 2, 5]
3
```

A function with a single string argument also accepts raw text. String results
are printed directly; numbers, booleans, arrays, and other JSON-compatible results
are JSON encoded. Variadic functions, generic entry points, and multiple return
values require your own `main`.

The existing `func Solve(input string) string` API still receives stdin unchanged.
Full programs with `func main()` still manage their own input and output.

Typed functions require tests in the input format above. Tasks that use custom
text formats can continue using the string API or a full program.
