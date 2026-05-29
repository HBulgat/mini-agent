# CODEBUDDY.md This file provides guidance to CodeBuddy when working with code in this repository.

## Project Status

This repository is a **design-locked, pre-implementation** Go project: `mini-agent`, a Claude-Code-style coding agent delivering both a CLI REPL and a React/AntD Web UI sharing one SQLite store. As of writing, only `go.mod` (`module github.com/HBulgat/mini-agent`, Go 1.23) and the `docs/` tree exist. The two top-level dirs `engine/` and `studio/` are empty placeholders that will be replaced by the canonical layout in `docs/system-design/01-overall-architecture.md` (`cmd/`, `internal/`, `web/`). Coding tasks must follow the locked design docs — do not invent module structure or scope.

## Where The Source Of Truth Lives

The `docs/` tree is **not** background reading; it is the spec and must be consulted before edits.

- `docs/requirements/` — what the product does/doesn't do. Status `Approved` (gated by phrase `同意需求分析结论`). Any scope change must go back through the requirements flow, never silently expanded in design or code.
- `docs/system-design/` — how it's built. Locked rounds: meta + R1–R6 + R7-1'. Pending rounds (R7-2, R8–R14) explicitly gate certain dev tasks (compaction algorithm, CLI slash dispatch, web API, trace format, MCP). Do not implement features whose design round is still ⏳ in `docs/dev-process/02-progress.md`.
- `docs/dev-process/02-progress.md` — single source of truth for task status; **must be updated on every completed task** (the file itself states this rule). Iteration order: Iter-0 → Iter-6, with Iter-7 (P3) outside the one-week window.
- `docs/system-design/02-key-decisions.md` — D1–D86 binding decisions. D31 in particular dictates Go pointer/value style (see below).
- `docs/system-design/ROADMAP.md` — what design rounds are still open.

When a task is blocked by a missing design round, stop and surface that fact rather than guessing.

## Common Commands

The Makefile is part of Iter-0 task T0.3 and not yet committed. Until it lands, use raw Go tooling. Once `Makefile` exists, prefer it (it will wrap the same commands).

- Build the whole module: `go build ./...`
- Run all tests: `go test ./...`
- Run one package's tests: `go test ./internal/tool/fs/...`
- Run a single test by name: `go test ./internal/agent/ -run TestLoop_StepLimit -v`
- Race detector for concurrency-heavy packages (agent loop, sync.Map failure counter): `go test -race ./internal/agent/...`
- Static checks: `go vet ./...` and `staticcheck ./...` (DoD §11 of the dev plan requires both clean).
- Tidy modules: `go mod tidy`
- Apply DB migrations (after Iter-0): `make migrate` — wraps `golang-migrate` against `internal/session/migrations/` and writes to `~/.mini-agent/data.db` by default.
- Regenerate type-safe queries: `make sqlc generate` — `sqlc.yaml` lives at repo root; generated code goes under `internal/session/store/`. Generated output and the migration DDL must be committed together (Risk R-4).
- Refresh tool JSON-Schema golden files: `make update-tool-goldens` (Iter-1 onward). Tool schemas use `invopop/jsonschema` reflection and are golden-file tested per R7-1' D84.
- Frontend dev server: `cd web && pnpm install && pnpm dev` (Vite + React 18 + TS + AntD v5 + `@ant-design/x`). Tests: `pnpm test` (Vitest).
- Run the CLI once compiled: `./mini-agent` (REPL) or `./mini-agent serve --port 7777` for the Web UI backend. `./mini-agent migrate` runs DB migrations.

The OpenAI-compatible provider used in P0 is **DeepSeek via base_url**, not openai.com. Configure under `llm.providers.openai_compat[]` in `~/.mini-agent/config.yaml`. CLI flags (`--model`, `--cwd`, `--yes`, `--auto-edit`, `--plan`, `--config`) override config; **environment variables are deliberately not read** as a config source.

## High-Level Architecture

### Layout And Dependency Rule

The project uses **flat functional packages** under `internal/` — no DDD layering, no DI framework. Each module defines its own interfaces in its own package (Java/TS-style "interface lives with provider"), then `internal/bootstrap` is the single package allowed to import everything and wire things together. The CLI entry is `cmd/mini-agent/main.go`. The React app lives in `web/` as a fully separate pnpm/Vite project.

The 9 core Go packages are: `trace` → `uio` → `llm` → `tool` → `permission` → `skill` → `compaction` → `session` → `agent`. `cli/repl`, `cli/cmd`, and `webapi` sit on top alongside `agent`. `agentsmd`, `config`, and `logs` are leaf utilities. The dependency graph in `docs/system-design/01-overall-architecture.md` §1.4 is the authoritative version — respect it to avoid cycles. In particular, `trace` must stay free of business deps, and `bootstrap` is the only legal "imports everything" package.

### The UIO Boundary (most important architectural concept)

`internal/uio` defines two interfaces — `Sink` (one-way: stream tokens, tool start/end, trace events; non-blocking) and `Prompter` (blocking: `AskApproval`, `AskUser`, `AskChoice`). `internal/cli/repl` and `internal/webapi` each implement both interfaces; the `agent`, `permission`, and tool packages depend only on the interfaces and never on stdin/stdout or HTTP details. Any feature that needs to talk to the user goes through these two interfaces — direct `fmt.Print` from agent/tool packages is a design violation. `Prompter` methods take `context.Context`; user interrupts (Ctrl+C in CLI, page close in Web) cancel via `ctx.Done()` and propagate up through the agent loop's "user interruption" branch (D63/D64). Web approvals are SSE-pushed cards plus a REST callback; CLI approvals are inline terminal prompts. Both must be behaviorally equivalent — a shared test suite is mandated by Risk R-5.

### Agent Engine (R6, doc 09)

`internal/agent` runs a ReAct loop with explicit step ceilings, a tool failure counter (`*sync.Map` keyed by a sha256 signature, D56/D57), per-call category bucketed parallelism for multiple tool calls in one assistant message (D58/D59), and `tool_use ↔ tool_result` pairing on interrupts (D64). The system prompt is composed of **three independent system messages** (D54): the built-in template, the merged AGENTS.md content wrapped in `<project_guidelines>` (D52), and the skill-list injection. System messages are **not persisted** (D55) — they are reconstructed on each turn. Subagents (`task` tool, depth ≤ 1) inherit the parent's loaded skills and surface failures via a structured template (D60/D61). When the user switches provider mid-session (`/model`), pending thinking blocks are archived with a summary (D62, see also doc 08 §8.11.4).

### Tool System (R7-1', doc 10)

All tools follow a unified template: a `tool.Tool` interface with `Name() / Description() / InputSchema() / Run(ctx, input, deps) (Result, error)`. Schemas are reflected from a Go struct via `invopop/jsonschema`; the registry validates schemas at startup (D84). `Result` carries `UserLimited` vs `ForcedTruncated` flags (separating "user passed `limit:N`" from "tool capped output"). Errors use a fixed `ErrorCode` enum (`ErrNotFound`, `ErrPermissionDenied`, `ErrTooLarge`, `ErrAmbiguous`, etc.) — these are part of the contract because the agent's failure counter signature includes the error code. `ctx` cancellation must immediately abort I/O and return `ctx.Err()` (strong contract per D63). Every tool ships with golden-file tests for its JSON schema and a shared `testkit` suite for `Result` invariants. Tool categories (only/write/exec/network/meta) drive the permission matrix, the parallel bucketing in the loop, and the four mode × tool table in `docs/system-design/04-tool-catalog.md`. The hard blacklist for `bash` is non-overridable, even under `--yes` (rm -rf /, fork bombs, system path writes — full list lives in shell tool implementation).

### Storage And Codecs (R3/R5, doc 06)

SQLite via `modernc.org/sqlite` (pure Go, no cgo). Schema migrations are SQL files under `internal/session/migrations/` embedded with `go:embed`; type-safe queries via `sqlc` into `internal/session/store/`. Messages have **two visibility axes** (D11–D24): `Visibility` (LLM-visible vs archived after compaction) and `UserVisibility` (normal/system/hidden — controls `/show-system`, `/show-hidden`, `/show-archived` slash commands). `Message.Content` is `[]ContentBlock` (text / tool_use / tool_result / thinking / redacted_thinking) — never a flat string. Each provider has a `Codec` that converts between provider-specific wire JSON and these neutral blocks; cache fields (`CachedPromptTokens`, `CacheCreationTokens`, `CacheReadTokens`) are stored in `usage_log` to support Anthropic prompt caching cost accounting.

### LLM Providers (R5, doc 08)

The `llm.Provider` interface unifies streaming and function calling across **OpenAI-compatible** (covers DeepSeek + OpenAI + any base_url-compatible service, multiple instances allowed), **Anthropic**, and **Gemini** (P1). Streams emit a typed event union including a `StreamBlockBoundary` event so consumers can correctly split text/thinking/tool_use boundaries. Thinking effort is per-request (`ThinkingEffort: "" | low | medium | high`); the `force_thinking` config option is for self-hosted reasoning models. Network layer does its own retries (3 attempts, exponential 1s/2s/4s with ±20% jitter) bounded by both per-request and total timeouts — not via an external retry library.

### Permissions (doc 07)

Four modes — default / `--auto-edit` / `--yes` / `--plan` — combine with three rule granularities (command, path, tool) loaded from `~/.mini-agent/permissions.yaml` (path is configurable). The `permission.Gate` is consulted before every tool call; the hard blacklist sits inside the gate and runs first, before any user-configured allow-list could possibly relax things.

## Conventions That Are Enforced By Design

- **Pointer/value style (D31)**: struct method receivers always `*T`; private helpers and slice batches use pointers; **public interface method parameters keep value types**; small high-frequency event structs passed to `Sink` keep value types; never apply `*T` to slice/map/channel/func/interface/string/error.
- **No environment variable config**: all settings come from defaults → `~/.mini-agent/config.yaml` → CLI flags. Don't add `os.Getenv` reads for behavior.
- **Sensitive fields in logs**: API keys must be filtered by the `internal/logs` slog handler; never log a `*Provider` config struct directly.
- **Update progress doc**: when a task in `docs/dev-process/02-progress.md` is finished, update its row, the dashboard counts, and append to the changelog. This is part of DoD §11.5, not optional.
- **Don't expand requirements in design or code**: if implementation reveals a missing requirement, stop and route through the requirements approval flow.
