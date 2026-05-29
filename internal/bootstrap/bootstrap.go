// Package bootstrap is the ONE place allowed to import every other
// internal package. It hand-wires components (no DI framework, see D3)
// into a runnable application.
//
// # Wiring topology (canonical, R1 §1.4)
//
//	config → logs → trace recorder
//	      → session.Store (open + migrate)
//	      → llm.Registry (per-provider construction)
//	      → tool.Registry (read_file in v1; rest land in T2.x)
//	      → permission.Gate (T2.1)
//	      → skill.Loader (Iter-4)
//	      → agentsmd.Loader (Iter-3)
//	      → compaction.Compactor (Iter-3)
//	      → agent.Loop (T2.6)
//	      → uio implementation (cli/repl T1.7 or webapi T5.x)
//
// # Status: v1 (T1.8)
//
// Only the lower half of the graph exists today; agent.Loop /
// permission / skill / compaction are still skeleton packages. Bootstrap
// v1 wires what *is* implemented and exposes it through *App for the
// CLI entry to validate plumbing before the REPL lands.
//
// The downstream packages (agent.Loop, permission.Gate, etc.) plug into
// the same App via additional fields once T2.x ships — the public
// surface is intentionally small and additive.

package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/HBulgat/mini-agent/internal/agent"
	"github.com/HBulgat/mini-agent/internal/agentsmd"
	"github.com/HBulgat/mini-agent/internal/config"
	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/llm/network"
	"github.com/HBulgat/mini-agent/internal/llm/openai"
	"github.com/HBulgat/mini-agent/internal/llm/tokenest"
	"github.com/HBulgat/mini-agent/internal/permission"
	"github.com/HBulgat/mini-agent/internal/session"
	"github.com/HBulgat/mini-agent/internal/session/store"
	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/ask"
	"github.com/HBulgat/mini-agent/internal/tool/fs"
	"github.com/HBulgat/mini-agent/internal/tool/search"
	"github.com/HBulgat/mini-agent/internal/tool/shell"
	"github.com/HBulgat/mini-agent/internal/trace"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// App is the assembled application: every subsystem the agent loop
// will eventually need is exposed as a public field so cli/cmd, webapi,
// and tests can pick the bits they need without re-importing the
// internal modules.
//
// The struct is deliberately a record of pointers to satisfy D31 (small
// shared structs go by value, but App is large + lives for the process
// lifetime so a single allocation is preferable to per-field copies).
type App struct {
	// Config is the fully-resolved configuration (defaults < file <
	// flags). Never mutate after BootstrapV1 returns.
	Config *config.Config

	// Logger is the process-wide slog handler. v1 returns a stdlib
	// text handler going to stderr; T0.5 (R12) replaces it with the
	// internal/logs slog handler that masks API keys.
	Logger *slog.Logger

	// Trace is the recorder every package writes to via
	// trace.WithTrace(ctx, App.Trace). NopRecorder until R12.
	Trace trace.Recorder

	// Repository is the SQLite-backed session store, already migrated
	// to the latest schema by store.OpenAndMigrate.
	Repository session.Repository

	// LLM is the provider registry. The active provider can be looked
	// up by name; the agent loop resolves Config.LLM.ActiveModel into
	// a (provider, model) pair via llm.SplitModelRef.
	LLM llm.Registry

	// Tools is the tool registry. v1 has read_file only; T2.x adds
	// the rest of the P0 set.
	Tools tool.Registry

	// Permission is the permission gate. Reads its rules from
	// cfg.Permission.RulesFile (~/.mini-agent/permissions.yaml by
	// default); built-in hard blacklist is always present. Mode is
	// the resolved cfg.Permission.Mode.
	Permission permission.Gate

	// Sink + Prompter are placeholder NopSink / NopPrompter in v1.
	// CLI replaces Sink with cli/repl's stdout streamer and Prompter
	// with the inline approval reader in T1.7; webapi swaps to its
	// SSE-based pair in T5.x.
	Sink     uio.Sink
	Prompter uio.Prompter

	// Agent is the ReAct loop. Constructed in step 6 once every
	// upstream dep is wired. Sub-agents (task tool), real
	// compaction, and skill activation use Nop placeholders that
	// later iterations swap in without changing the Loop API.
	Agent *agent.Loop

	// closers is the LIFO stack of cleanup callbacks accumulated as
	// we wire up subsystems. Close() drains them in reverse order so
	// e.g. the sqlite handle closes after every dependent has been
	// torn down.
	closers []func() error
}

// BootstrapV1 wires up everything implementable today and returns a
// ready-to-use *App. On any error along the way, partially-initialized
// subsystems are torn down before returning so callers don't leak file
// handles / goroutines on a failed boot.
//
// The function reads from cfg only; it never re-loads config.yaml or
// re-parses CLI flags. cli/cmd/root.go's PersistentPreRunE has already
// produced the canonical config.
func BootstrapV1(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, errors.New("bootstrap: nil config")
	}

	app := &App{Config: cfg}

	// Register a closer that runs all queued cleanups — used both by
	// the success path's app.Close and by the rollback below.
	cleanup := func() {
		for i := len(app.closers) - 1; i >= 0; i-- {
			_ = app.closers[i]()
		}
		app.closers = nil
	}

	// Step 1 — logger. Always succeeds; we'll harden it once R12 is
	// in (slog handler with API-key masking, lumberjack rotation).
	app.Logger = newDefaultLogger(cfg)

	// Step 2 — trace recorder. T1.1 left only the Nop implementation
	// in trace.NopRecorder; the file-backed recorder lives in R12.
	app.Trace = trace.NopRecorder{}

	// Step 3 — session repository (open file + run embedded
	// migrations). Failure here is almost always a path / permission
	// issue, so we surface a wrapped error explicitly.
	dbPath, err := expandUserPath(cfg.Storage.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: resolve database_path: %w", err)
	}
	repo, dbCloser, err := openRepository(dbPath)
	if err != nil {
		return nil, err
	}
	app.Repository = repo
	app.closers = append(app.closers, dbCloser)

	// Step 4 — llm provider registry. We register every configured
	// openai_compat instance; if none are present we fail early
	// because subsequent steps assume there's at least one provider
	// to talk to. (Anthropic / Gemini land in T4.9.)
	llmReg, err := buildLLMRegistry(cfg, app.Logger)
	if err != nil {
		cleanup()
		return nil, err
	}
	app.LLM = llmReg

	// Step 4.5 — uio fallbacks. We wire NopSink / NopPrompter
	// *before* the tool registry because ask_user holds the
	// prompter as a constructor dep. CLI / webapi swap these in
	// once their packages exist (T1.7 / T5.x). NopPrompter denies
	// every approval and refuses every AskUser by default, which
	// is the safe direction in --yes-less builds.
	app.Sink = uio.NopSink{}
	app.Prompter = uio.NopPrompter{}

	// Step 5 — tool registry. Ships every P0 tool the agent uses;
	// the Prompter is forwarded to ask_user.
	toolReg, err := buildToolRegistry(cfg, app.Prompter)
	if err != nil {
		cleanup()
		return nil, err
	}
	app.Tools = toolReg

	// Step 5.5 — permission gate. Loads ~/.mini-agent/permissions.yaml
	// (or whatever cfg.Permission.RulesFile points at) + the
	// built-in hard blacklist. Missing rules file = hard list only,
	// not an error (per R4 §7.3.1 fail-soft semantics).
	gate, err := buildGate(cfg)
	if err != nil {
		cleanup()
		return nil, err
	}
	app.Permission = gate

	// Step 5.6 — AGENTS.md loader. Built before agent.Loop so the
	// loop can pull guidance into prepareInitialHistory. Cfg
	// projects directly onto agentsmd.Config; missing files / read
	// errors are fail-soft inside the loader (R4 §7.2.5).
	agentsLoader := agentsmd.New(&agentsmd.Config{
		GlobalPath:    cfg.AgentsMD.GlobalPath,
		ProjectLookup: cfg.AgentsMD.ProjectLookup,
		// MaxBytes left at zero → DefaultMaxBytes (1 MiB) per D53.
	})

	// Step 6 — agent.Loop. Now that every dep above is wired we
	// can construct the ReAct loop. Compactor / SkillLoader still
	// use the package's Nop variants — Iter-3 / 4 land the real
	// implementations without changing this call site.
	loop, err := agent.New(agent.Deps{
		Provider:       app.LLM.Active(),
		Registry:       app.Tools,
		PermGate:       app.Permission,
		SessRepo:       app.Repository,
		Recorder:       app.Trace,
		AgentsMDLoader: agentsLoader,
		// Compactor / SkillLoader fall back to Nop variants when
		// nil — explicit-nil here documents the "to be wired
		// later" status.
	}, agent.Config{
		MaxSteps:     cfg.Agent.MaxSteps,
		ToolRetryMax: cfg.Agent.ToolRetryMax,
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("bootstrap: agent.New: %w", err)
	}
	app.Agent = loop

	return app, nil
}

// Close releases every subsystem in reverse construction order. Safe
// to call multiple times; each closer is invoked at most once.
func (a *App) Close() error {
	if a == nil {
		return nil
	}
	var firstErr error
	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	a.closers = nil
	return firstErr
}

// ============================================================
// Sub-builders (kept private — only BootstrapV1 calls them)
// ============================================================

// newDefaultLogger returns a slog.Logger writing JSON to stderr at the
// level requested by cfg.Log.Level. We keep this entirely separate
// from the future internal/logs implementation (R12) because v1 must
// not block on R12.
func newDefaultLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Log.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// openRepository opens the sqlite file (creating parent dirs as
// needed) and runs every pending migration. The returned closer is
// stashed onto app.closers; callers should NOT defer it themselves.
func openRepository(path string) (session.Repository, func() error, error) {
	if path == "" {
		return nil, nil, errors.New("bootstrap: storage.database_path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("bootstrap: mkdir for database: %w", err)
	}
	db, err := store.OpenAndMigrate(path)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap: open+migrate sqlite at %s: %w", path, err)
	}
	// store.New wraps *sql.DB into a Repository; we hold the *sql.DB
	// reference only via the closer so misuse via app.Repository is
	// impossible.
	repo := store.New(db)
	return repo, db.Close, nil
}

// buildLLMRegistry constructs every configured openai_compat provider
// in order, registering each into a fresh llm.Registry. If the active
// model spec ("provider:model") references a known provider we set it
// active; otherwise the first registered provider becomes active.
//
// We skip Anthropic / Gemini for v1 — their providers ship in T4.9.
func buildLLMRegistry(cfg *config.Config, logger *slog.Logger) (llm.Registry, error) {
	reg := llm.NewRegistry()
	estimator := tokenest.NewCharRatio(0.6, 0.25) // §8.9.2 baseline weights

	openaiCompat := cfg.LLM.Providers.OpenAICompat
	if len(openaiCompat) == 0 {
		return nil, errors.New("bootstrap: no LLM providers configured (need at least one llm.providers.openai_compat[] entry)")
	}

	for _, oc := range openaiCompat {
		if oc == nil {
			continue
		}
		ocCfg := &openai.Config{
			Name:          oc.Name,
			BaseURL:       oc.BaseURL,
			APIKey:        oc.APIKey,
			DefaultModel:  oc.DefaultModel,
			ForceThinking: oc.ForceThinking,
		}
		// Wire the network defaults from cfg.LLM.Network so per-
		// provider overrides land in one place. Provider's New()
		// will fall back to network.Default* if these stay nil.
		if cfg.LLM.Network.RequestTimeout > 0 || cfg.LLM.Network.TotalTimeout > 0 {
			ocCfg.Timeout = &network.TimeoutConfig{
				Request: cfg.LLM.Network.RequestTimeout,
				Total:   cfg.LLM.Network.TotalTimeout,
			}
		}
		if cfg.LLM.Network.RetryMax > 0 {
			ocCfg.Retry = &network.RetryConfig{
				Max:         cfg.LLM.Network.RetryMax,
				BackoffBase: cfg.LLM.Network.RetryBackoffBase,
				Jitter:      cfg.LLM.Network.RetryJitter,
			}
		}

		if err := ocCfg.Validate(); err != nil {
			return nil, fmt.Errorf("bootstrap: provider %q: %w", oc.Name, err)
		}
		prov, err := openai.New(ocCfg, estimator, logger.With(slog.String("provider", oc.Name)))
		if err != nil {
			return nil, fmt.Errorf("bootstrap: build provider %q: %w", oc.Name, err)
		}
		if err := reg.Register(prov); err != nil {
			return nil, fmt.Errorf("bootstrap: register provider %q: %w", oc.Name, err)
		}
	}

	// Best-effort active selection: try to honour cfg.LLM.ActiveModel
	// (in either "provider:model" or bare "model" form). On miss, the
	// first registered provider becomes active by default. Calling
	// SetActive("") falls back to the first-registered provider per
	// memRegistry's documented behaviour, so we always end up with a
	// deterministic active provider even when active_model is unset.
	active := strings.TrimSpace(cfg.LLM.ActiveModel)
	if err := reg.SetActive(active); err != nil {
		// active_model was set but didn't match anything; fall back
		// to first-registered with a warning instead of failing the
		// whole boot.
		if active != "" {
			logger.Warn("bootstrap: active_model not resolvable; falling back to first registered provider",
				slog.String("active_model", active),
				slog.String("error", err.Error()))
			if err2 := reg.SetActive(""); err2 != nil {
				return nil, fmt.Errorf("bootstrap: SetActive fallback: %w", err2)
			}
		} else {
			return nil, fmt.Errorf("bootstrap: SetActive: %w", err)
		}
	}
	return reg, nil
}

// networkTimeoutFromCfg / networkRetryFromCfg used to live here as
// thin projections from config.NetworkCfg into network.{Timeout,Retry}Config.
// Inlined into buildLLMRegistry once the field name confusion was
// resolved — kept the comment as a breadcrumb in case someone tries to
// re-extract them.

// buildToolRegistry registers every implemented tool. v1 ships the
// four filesystem tools that already follow the R7-1' template
// (read_file, write_file, edit_file, delete_file). T2.4+T2.5 will
// append the rest of the P0 set (list_dir, grep, glob, bash, ask_user)
// in alphabetical-by-name order so the generated system-prompt
// catalog is stable.
//
// The cwd callback closes over os.Getwd as a v1 fallback. T2.6 will
// replace it with a session.Cwd lookup so `/cwd` switches in the REPL
// take effect across all tools.
func buildToolRegistry(cfg *config.Config, prompter uio.Prompter) (tool.Registry, error) {
	reg := tool.NewRegistry()

	cwdProvider := func() string {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return ""
	}

	// Register fs tools in alphabetical order (matches §10.7's table
	// and produces a deterministic system-prompt catalogue).
	registrations := []struct {
		name string
		t    tool.Tool
	}{
		{"ask_user", ask.NewAskUser(prompter)},
		{"bash", shell.NewBash(cwdProvider)},
		{"delete_file", fs.NewDeleteFile(cwdProvider)},
		{"edit_file", fs.NewEditFile(cwdProvider)},
		{"glob", search.NewGlob(cwdProvider)},
		{"grep", search.NewGrep(cwdProvider)},
		{"list_dir", fs.NewListDir(cwdProvider)},
		{"read_file", fs.NewReadFile(cwdProvider)},
		{"write_file", fs.NewWriteFile(cwdProvider)},
	}
	for _, r := range registrations {
		if err := reg.Register(r.t); err != nil {
			return nil, fmt.Errorf("bootstrap: register %s: %w", r.name, err)
		}
	}
	return reg, nil
}

// buildGate constructs the permission.Gate by loading the user rules
// file (if any) and merging with the built-in hard blacklist. Mode
// comes from cfg.Permission.Mode after the standard PermissionMode
// (string) → tool.Mode (int) projection.
//
// rulesFile is path-expanded ("~/" / "$HOME/") so a config snippet
// like `rules_file: ~/.mini-agent/permissions.yaml` works without the
// user having to write the absolute path.
func buildGate(cfg *config.Config) (permission.Gate, error) {
	rulesPath, err := expandUserPath(cfg.Permission.RulesFile)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: resolve rules_file: %w", err)
	}
	rules, err := permission.LoadRules(rulesPath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: load permission rules: %w", err)
	}

	cwd, _ := os.Getwd() // best-effort; gate's substitutor handles "" gracefully

	gate, err := permission.NewGate(rules, mapPermissionMode(cfg.Permission.Mode), cwd)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: new gate: %w", err)
	}
	return gate, nil
}

// mapPermissionMode converts the YAML-friendly string enum
// (config.PermissionMode) into the typed tool.Mode used at runtime.
// Unknown / empty values fall back to ModeDefault — config.validate
// already rejects truly bogus strings, but we fail safe here as well.
func mapPermissionMode(m config.PermissionMode) permission.Mode {
	switch m {
	case config.ModeAutoEdit:
		return permission.ModeAutoEdit
	case config.ModeYes:
		return permission.ModeYes
	case config.ModePlan:
		return permission.ModePlan
	default:
		return permission.ModeDefault
	}
}

// expandUserPath rewrites a leading "~/" or "$HOME/" to the resolved
// home directory. config.Load already does this for known fields, but
// we double-guard here because config.StorageCfg uses a raw string and
// nothing forces the path to be expanded.
func expandUserPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	if strings.HasPrefix(p, "$HOME/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[len("$HOME/"):]), nil
	}
	return p, nil
}
