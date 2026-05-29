# mini-agent

A Claude-Code-style coding agent built in Go, delivering both a CLI REPL and
a React/AntD Web UI on top of one SQLite store.

> **Status: pre-implementation.** Only the project skeleton (T0.1) currently
> exists. All architectural and behavioural decisions are locked in
> `docs/system-design/` (R1–R6 + R7-1', 86 key decisions). Iter-0 is in
> progress; tracking lives in [`docs/dev-process/02-progress.md`](docs/dev-process/02-progress.md).

## Documentation

The canonical sources of truth, in reading order:

1. [`docs/requirements/`](docs/requirements/) — what we build (Approved).
2. [`docs/system-design/`](docs/system-design/) — how we build it.
   - [`README.md`](docs/system-design/README.md) — index + reading order
   - [`02-key-decisions.md`](docs/system-design/02-key-decisions.md) — D1–D86
   - [`ROADMAP.md`](docs/system-design/ROADMAP.md) — pending design rounds
3. [`docs/dev-process/`](docs/dev-process/) — execution.
   - [`01-development-plan.md`](docs/dev-process/01-development-plan.md)
   - [`02-progress.md`](docs/dev-process/02-progress.md) — single source of
     truth for task status. Updated on every completed task.

## Quick Start

The Makefile is part of T0.3 (Iter-0) and not yet committed. Until it lands,
use raw Go tooling:

```bash
go build ./...                       # build everything
go test ./...                        # run all tests
go vet ./...                         # static checks
go test -race ./internal/agent/...   # race detector for concurrency hot paths
go mod tidy                          # tidy modules
```

Frontend dev server (Iter-0 T0.7 onward):

```bash
cd web && pnpm install && pnpm dev
```

## Layout

Flat functional packages under `internal/`, plus the cobra entrypoint
`cmd/mini-agent/main.go` and a separate React project in `web/`. Module
dependency graph and full directory tree are documented in
[`docs/system-design/01-overall-architecture.md`](docs/system-design/01-overall-architecture.md).

| Path | Role |
|---|---|
| `cmd/mini-agent/` | process entrypoint (cobra root) |
| `internal/agent/` | ReAct loop + sub-agent dispatch |
| `internal/llm/` | Provider interface + OpenAI / Anthropic / Gemini |
| `internal/tool/` | Tool interface + registry + 14 built-in tools |
| `internal/session/` | Session / Message / Todo + SQLite repository |
| `internal/uio/` | Sink + Prompter (CLI/Web boundary) |
| `internal/cli/` | REPL loop + cobra commands |
| `internal/webapi/` | gin REST + SSE for the Web UI |
| `internal/bootstrap/` | the only package allowed to import everything |
| `web/` | React + Vite + AntD app |

## Conventions

- Pointer/value style — see D31. Receivers `*T`; private slice batches
  pointer; public interface params keep value types; never `*` for
  slice/map/channel/func/interface/string/error.
- No env-var config — settings flow defaults → `~/.mini-agent/config.yaml`
  → CLI flags. Don't add `os.Getenv` reads for behaviour.
- Sensitive fields in logs — API keys filtered by `internal/logs`; never
  log a `*Provider` config struct directly.
- Update `docs/dev-process/02-progress.md` on every completed task — DoD
  §11.5 of the dev plan, not optional.

## License

TBD.
