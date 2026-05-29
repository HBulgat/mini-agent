// Package cmd hosts the mini-agent cobra command tree.
//
// Command map (D8 / D9 / D15):
//
//	mini-agent              -> drop into REPL (default root command)
//	mini-agent -p "..."     -> one-shot prompt mode (T1.7)
//	mini-agent serve        -> start Web UI backend (T5.4)
//	mini-agent migrate      -> run DB migrations explicitly (T1.5; auto-run on
//	                           every startup as well per D15)
//	mini-agent version      -> print build info
//
// T0.6 scope: only the skeleton — flag wiring, help text, exit codes.
// Real behavior (REPL loop, gin server, sqlite migrate.Up) is filled in by
// later tasks; for now the placeholder runners emit a clear "pending TXX"
// message and return a non-zero exit code so they can't be mistaken for
// working features.
package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/HBulgat/mini-agent/internal/bootstrap"
	"github.com/HBulgat/mini-agent/internal/config"
)

// Build-time info. main.go forwards values injected via -ldflags.
//
// We keep a package-level set so subcommands (`version`) can read them
// without threading them through every Cobra hook.
var (
	buildVersion = "0.0.0-dev"
	buildCommit  = "none"
	buildTime    = "unknown"
)

// SetBuildInfo lets cmd/mini-agent/main.go forward the ldflags-injected
// values into the cmd package before Execute runs.
func SetBuildInfo(version, commit, when string) {
	if version != "" {
		buildVersion = version
	}
	if commit != "" {
		buildCommit = commit
	}
	if when != "" {
		buildTime = when
	}
}

// Persistent CLI flags shared by every subcommand. These are bound on the
// root command so every leaf inherits them. Resolved into a *config.Config
// by PersistentPreRunE before any subcommand RunE fires.
type rootFlags struct {
	configPath string // --config
	model      string // --model (provider:model or bare model)
	cwd        string // --cwd
	yes        bool   // --yes        \
	autoEdit   bool   // --auto-edit   } resolved into config.PermissionMode
	plan       bool   // --plan       /
	printMode  bool   // -p / --print  one-shot mode (D9)
	prompt     string // raw prompt text when -p is used (positional or via flag)

	// cfg is populated by PersistentPreRunE. Subcommand RunE handlers
	// must read it via flags.cfg, never re-load the file. nil iff the
	// subcommand opted out of config loading (`version`).
	cfg *config.Config
}

// Execute is the single entry point used by cmd/mini-agent/main.go.
// It returns the desired process exit code instead of calling os.Exit
// directly so tests can drive it.
func Execute() int {
	return run(os.Stdout, os.Stderr, os.Args[1:])
}

// run is the testable core: parses argv, dispatches to cobra, returns an
// exit code. We set SilenceErrors=true on the root so cobra doesn't add
// its own "Error:" prefix on top of ours; in exchange we print the error
// to stderr ourselves.
func run(stdout, stderr io.Writer, args []string) int {
	flags := &rootFlags{}
	root := newRootCmd(flags)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, "mini-agent:", err)
		return 1
	}
	return 0
}

// newRootCmd builds the entire command tree. It is split out so tests can
// instantiate a fresh tree per case (cobra commands carry mutable state).
func newRootCmd(flags *rootFlags) *cobra.Command {
	root := &cobra.Command{
		Use:   "mini-agent",
		Short: "Claude-Code-style coding agent (CLI + Web UI)",
		Long: `mini-agent is a local-first coding assistant that runs a ReAct
agent loop against pluggable LLM providers (OpenAI-compatible, Anthropic,
Gemini) with a unified tool catalog (read_file, write_file, bash, ...) and
a permission gate (default / --auto-edit / --yes / --plan).

Run with no arguments to drop into the interactive REPL.
Use ` + "`mini-agent serve`" + ` to start the Web UI backend.`,
		// Suppress cobra's automatic "Error:" prefix and usage dump on
		// runtime errors — our subcommands print their own messages.
		SilenceErrors: true,
		SilenceUsage:  true,

		// PersistentPreRunE loads + flag-overlays the config exactly
		// once per process invocation. Subcommands that don't need a
		// Config (currently only `version`) opt out by setting the
		// `cmd-skip-config` annotation.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Annotations[skipConfigAnnotation] == "true" {
				return nil
			}
			return loadConfigInto(flags)
		},
		// The default action (no subcommand) launches the REPL or the
		// one-shot prompt path depending on -p.
		RunE: func(cmd *cobra.Command, args []string) error {
			// `-p "do X"` / positional prompt accumulation is finalized
			// when REPL lands (T1.7). For now we just record the intent.
			if len(args) > 0 && flags.prompt == "" {
				flags.prompt = args[0]
			}
			return runREPL(cmd, flags)
		},
		// Allow positional args so `mini-agent -p "fix bug"` and
		// `mini-agent "fix bug"` both work once T1.7 lands.
		Args: cobra.ArbitraryArgs,
	}

	// --- persistent flags (inherited by every subcommand) -----------------
	pf := root.PersistentFlags()
	pf.StringVar(&flags.configPath, "config", "",
		"path to config file (default ~/.mini-agent/config.yaml)")
	pf.StringVar(&flags.model, "model", "",
		"override active model, e.g. `deepseek:deepseek-chat` or bare model name")
	pf.StringVar(&flags.cwd, "cwd", "",
		"override working directory the agent operates on")
	pf.BoolVar(&flags.yes, "yes", false,
		"approve every non-blacklisted action automatically (DANGEROUS)")
	pf.BoolVar(&flags.autoEdit, "auto-edit", false,
		"auto-approve file edits inside cwd; still prompt for shell/network")
	pf.BoolVar(&flags.plan, "plan", false,
		"plan-only mode: read-only tools allowed, no writes/exec")

	// -p / --print is technically root-only (per D9) but cobra has no
	// "local-only" persistent flag; we put it on the root's local flags
	// instead so subcommands don't accidentally inherit it.
	root.Flags().BoolVarP(&flags.printMode, "print", "p", false,
		"one-shot mode: read prompt from args, print final answer, exit")

	// --- subcommands -------------------------------------------------------
	root.AddCommand(newVersionCmd())
	root.AddCommand(newMigrateCmd(flags))
	root.AddCommand(newServeCmd(flags))

	return root
}

// runREPL is the placeholder root action. T1.7 will replace this with
// the real interactive loop. Until then we still want operators to see
// that bootstrap wiring works end-to-end, so we run BootstrapV1 here
// and print a brief diagnostic before returning a non-zero exit.
//
// Returning a non-zero exit on the placeholder path is intentional —
// the existing TestPlaceholderSubcommandsExitNonZero contract assumes
// it, and we want CI to fail loudly if someone accidentally treats the
// stub as a working REPL.
func runREPL(cmd *cobra.Command, flags *rootFlags) error {
	out := cmd.OutOrStdout()
	if flags.cfg == nil {
		return fmt.Errorf("REPL: configuration not loaded")
	}

	app, err := bootstrap.BootstrapV1(flags.cfg)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	defer func() { _ = app.Close() }()

	// Brief assembly report: the operator can confirm that providers,
	// tools, and the SQLite store all wired up. Cosmetic — the real
	// work will be the interactive loop in T1.7.
	fmt.Fprintln(out, "mini-agent REPL skeleton — interactive loop pending T1.7.")
	fmt.Fprintf(out, "  database:  %s\n", flags.cfg.Storage.DatabasePath)
	fmt.Fprintf(out, "  providers: %d registered\n", len(app.LLM.List()))
	fmt.Fprintf(out, "  tools:     %d registered\n", len(app.Tools.List()))
	fmt.Fprintf(out, "  mode:      %s\n", app.Permission.GetMode())
	if active := app.LLM.Active(); active != nil {
		caps := active.Capabilities()
		fmt.Fprintf(out, "  active:    %s/%s\n", active.Name(), caps.Model)
	}

	if flags.printMode {
		fmt.Fprintln(out, "(-p one-shot mode also pending T1.7.)")
	}
	return fmt.Errorf("REPL not yet implemented (T1.7)")
}

// skipConfigAnnotation marks a subcommand as "does not require a parsed
// Config". We use cobra's command-level Annotations map (a string→string
// table) to keep the opt-out declarative and discoverable.
const skipConfigAnnotation = "mini-agent.skip-config"

// loadConfigInto resolves --yes / --auto-edit / --plan, loads the file,
// applies overrides, and stashes the result back on flags.cfg.
//
// Errors here surface to cobra and become the process exit code; we
// never silently fall back to defaults when a user-specified --config
// or invalid flag combination is involved.
func loadConfigInto(flags *rootFlags) error {
	mode, err := config.ResolveMode(flags.yes, flags.autoEdit, flags.plan)
	if err != nil {
		return err
	}

	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return err
	}

	overrides := &config.FlagOverrides{Mode: mode}
	if flags.model != "" {
		m := flags.model
		overrides.Model = &m
	}
	if flags.cwd != "" {
		c := flags.cwd
		overrides.Cwd = &c
	}

	cfg, err = config.ApplyFlags(cfg, overrides)
	if err != nil {
		return err
	}

	flags.cfg = &cfg
	return nil
}
