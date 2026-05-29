package cmd

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// runWithArgs builds a fresh command tree, executes it with the given
// args, and returns (stdout, stderr, exitCode). Mirrors Execute() but
// lets tests inspect output buffers.
//
// Every test that exercises root.Execute MUST call isolateHome first so
// PersistentPreRunE doesn't accidentally read the developer's real
// ~/.mini-agent/config.yaml — that would make tests host-dependent.
func runWithArgs(args []string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := run(&stdout, &stderr, args)
	return stdout.String(), stderr.String(), code
}

// isolateHome points os.UserHomeDir() at a per-test temp dir. Without
// this, tests that go through PersistentPreRunE would either pick up
// the developer's real config or fail when the env lacks HOME.
func isolateHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("USERPROFILE", tmp)
	default:
		t.Setenv("HOME", tmp)
	}
}

// TestVersionCommand verifies the version subcommand prints the injected
// build info and exits with code 0.
func TestVersionCommand(t *testing.T) {
	SetBuildInfo("v9.9.9-test", "deadbeef", "2026-05-24T00:00:00Z")
	t.Cleanup(func() { SetBuildInfo("0.0.0-dev", "none", "unknown") })

	stdout, _, code := runWithArgs([]string{"version"})
	if code != 0 {
		t.Fatalf("version exit=%d, want 0", code)
	}
	for _, want := range []string{"v9.9.9-test", "deadbeef", "2026-05-24T00:00:00Z", "go"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("version stdout missing %q; got:\n%s", want, stdout)
		}
	}
}

// TestRootHelp verifies --help works on the root and lists every
// subcommand the spec mandates (D8).
func TestRootHelp(t *testing.T) {
	stdout, _, code := runWithArgs([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help exit=%d, want 0", code)
	}
	for _, want := range []string{"version", "serve", "migrate"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("root --help missing subcommand %q; got:\n%s", want, stdout)
		}
	}
}

// TestPlaceholderSubcommandsExitNonZero ensures unfinished subcommands
// surface a non-zero exit code so they can't be mistaken for working
// features. As tasks land (T1.5 migrate; T1.7 REPL; T5.4 serve), drop
// the corresponding case from this list.
func TestPlaceholderSubcommandsExitNonZero(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"root REPL placeholder", []string{}},
		// `migrate` was implemented in T1.5 and now exits 0 on success.
		{"serve placeholder", []string{"serve"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			_, _, code := runWithArgs(tc.args)
			if code == 0 {
				t.Fatalf("%s exit=0, want non-zero (placeholder must not look successful)", tc.name)
			}
		})
	}
}

// TestMigrateCommand_RealRun verifies the real migrate path: with an
// isolated HOME, `mini-agent migrate` should create a fresh ~/.mini-agent/
// directory, open the SQLite file, apply migrations, and exit 0 with
// a status line on stdout.
func TestMigrateCommand_RealRun(t *testing.T) {
	isolateHome(t)
	stdout, stderr, code := runWithArgs([]string{"migrate"})
	if code != 0 {
		t.Fatalf("migrate exit=%d (want 0). stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "schema version") {
		t.Errorf("expected schema-version status line; got:\n%s", stdout)
	}
}

// TestPersistentFlagsParsed verifies the root's persistent flags are
// accepted on every subcommand without error (we use `version` because
// it succeeds; if cobra rejects an unknown flag the exit code would be
// non-zero).
func TestPersistentFlagsParsed(t *testing.T) {
	isolateHome(t)
	args := []string{
		"--config", "/tmp/x.yaml",
		"--model", "deepseek:deepseek-chat",
		"--cwd", "/tmp",
		"--auto-edit",
		"version", // version skips config loading, so missing file is fine
	}
	stdout, stderr, code := runWithArgs(args)
	if code != 0 {
		t.Fatalf("persistent flags rejected: exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "mini-agent ") {
		t.Errorf("expected version output, got:\n%s", stdout)
	}
}

// TestServeFlags verifies serve --help advertises --host / --port.
func TestServeFlags(t *testing.T) {
	stdout, _, code := runWithArgs([]string{"serve", "--help"})
	if code != 0 {
		t.Fatalf("serve --help exit=%d", code)
	}
	for _, want := range []string{"--host", "--port"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("serve --help missing %q; got:\n%s", want, stdout)
		}
	}
}

// TestPreRun_ConflictingModeFlags verifies PersistentPreRunE rejects
// mutually-exclusive --yes / --auto-edit / --plan combos before any
// subcommand RunE fires.
func TestPreRun_ConflictingModeFlags(t *testing.T) {
	isolateHome(t)
	// We use `serve` because it's still a placeholder — the conflict
	// check should fire in PreRunE and never reach the leaf RunE.
	_, _, code := runWithArgs([]string{"--yes", "--plan", "serve"})
	if code == 0 {
		t.Fatal("expected non-zero exit for --yes --plan combination")
	}
}

// TestPreRun_ExplicitMissingConfigFails verifies that pointing --config
// at a nonexistent file fails fast with a non-zero exit, instead of
// silently falling back to defaults.
func TestPreRun_ExplicitMissingConfigFails(t *testing.T) {
	isolateHome(t)
	_, _, code := runWithArgs([]string{"--config", "/no/such/file.yaml", "serve"})
	if code == 0 {
		t.Fatal("expected non-zero exit when --config points at a missing file")
	}
}

// TestPreRun_VersionSkipsConfig verifies the `version` subcommand does
// not error even when --config points at a missing file (D86-style: tooling
// must always report its own version, even on a broken install).
func TestPreRun_VersionSkipsConfig(t *testing.T) {
	stdout, _, code := runWithArgs([]string{"--config", "/no/such/file.yaml", "version"})
	if code != 0 {
		t.Fatalf("version with broken --config: exit=%d", code)
	}
	if !strings.Contains(stdout, "mini-agent ") {
		t.Errorf("version stdout missing prefix; got:\n%s", stdout)
	}
}
