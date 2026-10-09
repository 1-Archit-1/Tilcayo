# 1. The orchestrator classifies worker failures

## Status

Accepted, 2026-10-08. Part of [Decide: worker / control-plane contract (incl. shared vs per-engine entrypoint)](https://github.com/1-Archit-1/Tilcayo/issues/3).

## Context

A worker's error reaches the control plane only through the tail of its stderr log, capped at 2048 bytes or 80 lines. In Kubernetes mode this is `FallbackToLogsOnError` filling the termination message; Docker mode reads the log tail with the same cap. Whatever the worker prints last survives.

Warnings share stderr with errors, for example the warning about stray `AWS_*` variables and the cgroup v1 warning.

Not every failure is the worker's own. In Docker mode, kills show only as exit codes: 137 with `OOMKilled=true` for out of memory, 137 with `OOMKilled=false` for a timeout, 143 for a cancel. In Kubernetes mode a timeout or cancel deletes the pod, so there is no exit code or message at all. The only signal is the Job condition `DeadlineExceeded` and, for a cancel, the `tilcayo.io/cancelled` annotation. Out of memory shows as the `OOMKilled` termination reason.

Some worker exits also bypassed the error helper `fail()`: a missing `QUERY` in both entrypoints and the `embed()` error path in the ml entrypoint. An uncaught exception exited 1 with a traceback and no marker.

## Decision

The failure kind comes from orchestrator signals only. The worker's output only carries the message.

| Signal | Docker | K8s | Job state / reason |
|---|---|---|---|
| success | exit 0 | Job `Complete` | `succeeded` |
| OOM | 137 + `OOMKilled=true` | container reason `OOMKilled` | `failed`, "out of memory" |
| timeout | 137 + `OOMKilled=false` | `DeadlineExceeded`, no annotation | `failed`, "timed out" |
| cancel | 143 | `DeadlineExceeded` + `tilcayo.io/cancelled=true` | `cancelled` |
| worker error | exit 1 | `Failed`, exit 1 | `failed`, message from sentinel |
| anything else | other nonzero (e.g. 139) | other exit code | `failed`, "worker crashed (exit N)" + raw tail |

On any worker failure, the final stderr line is `tilcayo-error: <message>`, with newlines escaped as `\n` and truncated to about 1 KiB. The multi-line detail is printed above it. Every exit path goes through `fail()`, and a `sys.excepthook` turns uncaught exceptions into `fail()`.

The control plane parses only the last line of the tail. Exit 1 without the sentinel means `failed`, "worker failed without a message", with the raw tail attached. Any other nonzero exit is a crash.

## Alternatives considered

- **The worker reports its own failure kind.** Not possible: the worker never sees out of memory, timeout or cancel, and in Kubernetes mode the pod is already gone.
- **Infer the kind from the error text.** Rejected: DuckDB errors can echo user input, across lines, so the text cannot be trusted.
- **An error file or termination-log file.** Rejected earlier in [Decide: Docker-mode equivalents for K8s-specific job mechanics](https://github.com/1-Archit-1/Tilcayo/issues/11).

## Consequences

- Every worker exit path goes through one helper plus an excepthook. A new exit path that skips it shows up as "worker failed without a message".
- The detail strings in `workers/test_security.sh` change, and its grep is updated in the same commit.
- A new failure kind must be added to the table. For example, adding a disk limit later would bring pod eviction, which this table does not cover yet.
