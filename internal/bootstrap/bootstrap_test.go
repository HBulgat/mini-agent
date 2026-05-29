package bootstrap

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HBulgat/mini-agent/internal/config"
)

// ============================================================
// expandUserPath
// ============================================================

func TestExpandUserPath(t *testing.T) {
	// Drive HOME to a known value so the helper's output is
	// deterministic across machines.
	home := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("USERPROFILE", home)
	default:
		t.Setenv("HOME", home)
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"absolute pass-through", "/etc/passwd", "/etc/passwd"},
		{"relative pass-through", "data.db", "data.db"},
		{"tilde expansion", "~/foo/bar.db", filepath.Join(home, "foo", "bar.db")},
		{"$HOME expansion", "$HOME/foo.db", filepath.Join(home, "foo.db")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := expandUserPath(c.in)
			if err != nil {
				t.Fatalf("expandUserPath(%q) error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("expandUserPath(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

// ============================================================
// BootstrapV1 — happy path
// ============================================================

// validCfg builds a minimal Config that BootstrapV1 will accept. The
// API key is placeholder; openai.Config.Validate only checks non-empty,
// not real-network usability.
func validCfg(dbPath string) *config.Config {
	return &config.Config{
		LLM: config.LLMCfg{
			ActiveModel: "deepseek:deepseek-chat",
			Providers: config.ProvidersCfg{
				OpenAICompat: []*config.OpenAICompatCfg{
					{
						Name:         "deepseek",
						BaseURL:      "https://api.deepseek.com/v1",
						APIKey:       "test-api-key-not-real",
						DefaultModel: "deepseek-chat",
					},
				},
			},
		},
		Permission: config.PermissionCfg{Mode: config.ModeDefault},
		Storage:    config.StorageCfg{DatabasePath: dbPath},
		Log:        config.LogCfg{Level: "warn"}, // suppress info-level chatter in tests
	}
}

func TestBootstrapV1_Happy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	cfg := validCfg(dbPath)

	app, err := BootstrapV1(cfg)
	if err != nil {
		t.Fatalf("BootstrapV1 happy path failed: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	if app.Config != cfg {
		t.Error("App.Config should be the passed-in cfg")
	}
	if app.Logger == nil {
		t.Error("App.Logger must not be nil")
	}
	if app.Trace == nil {
		t.Error("App.Trace must not be nil (NopRecorder is fine)")
	}
	if app.Repository == nil {
		t.Error("App.Repository must not be nil")
	}
	if app.LLM == nil {
		t.Error("App.LLM must not be nil")
	}
	if app.Tools == nil {
		t.Error("App.Tools must not be nil")
	}
	if app.Sink == nil || app.Prompter == nil {
		t.Error("App.Sink and App.Prompter must not be nil (Nop fallbacks)")
	}
	if app.Permission == nil {
		t.Error("App.Permission must not be nil")
	}
	if app.Agent == nil {
		t.Error("App.Agent must not be nil")
	}

	// Provider lookup
	if got := len(app.LLM.List()); got != 1 {
		t.Errorf("LLM.List() len=%d, want 1", got)
	}
	if active := app.LLM.Active(); active == nil {
		t.Error("LLM.Active() should be non-nil after registration")
	} else if active.Name() != "deepseek" {
		t.Errorf("LLM.Active().Name()=%q, want deepseek", active.Name())
	}

	// Tool registry — every fs tool that follows the R7-1' template
	// gets registered in v1. We assert the exact roster so a missing
	// or extra registration trips the test instead of silently
	// changing the system-prompt catalogue.
	tools := app.Tools.List()
	gotNames := make([]string, len(tools))
	for i, tl := range tools {
		gotNames[i] = tl.Name()
	}
	wantNames := []string{"ask_user", "bash", "delete_file", "edit_file", "glob", "grep", "list_dir", "read_file", "write_file"}
	if !equalStringSlices(gotNames, wantNames) {
		t.Errorf("tool roster mismatch:\n  got %v\n want %v", gotNames, wantNames)
	}
}

// TestBootstrapV1_NoProviders verifies we fail loudly when the config
// has zero LLM providers — silent fallback to a useless registry would
// hide the misconfiguration until the agent loop tries to call Stream().
func TestBootstrapV1_NoProviders(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	cfg := &config.Config{
		Permission: config.PermissionCfg{Mode: config.ModeDefault},
		Storage:    config.StorageCfg{DatabasePath: dbPath},
		Log:        config.LogCfg{Level: "warn"},
	}

	_, err := BootstrapV1(cfg)
	if err == nil {
		t.Fatal("expected BootstrapV1 to fail with no providers configured")
	}
	if !contains(err.Error(), "no LLM providers configured") {
		t.Errorf("error message should mention missing providers; got: %v", err)
	}
}

// TestBootstrapV1_MissingAPIKey: the openai.Config.Validate should
// reject a provider with no API key, and bootstrap must surface the
// failure as a clean error (not a panic / nil dereference).
func TestBootstrapV1_MissingAPIKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	cfg := &config.Config{
		LLM: config.LLMCfg{
			Providers: config.ProvidersCfg{
				OpenAICompat: []*config.OpenAICompatCfg{
					{
						Name:         "broken",
						BaseURL:      "https://example.com",
						APIKey:       "", // <-- intentional
						DefaultModel: "gpt-4o",
					},
				},
			},
		},
		Permission: config.PermissionCfg{Mode: config.ModeDefault},
		Storage:    config.StorageCfg{DatabasePath: dbPath},
		Log:        config.LogCfg{Level: "warn"},
	}

	_, err := BootstrapV1(cfg)
	if err == nil {
		t.Fatal("expected BootstrapV1 to fail when a provider has empty APIKey")
	}
	if !contains(err.Error(), "broken") {
		t.Errorf("error should reference the broken provider name; got: %v", err)
	}
}

// TestBootstrapV1_NilCfg: defensive nil check.
func TestBootstrapV1_NilCfg(t *testing.T) {
	_, err := BootstrapV1(nil)
	if err == nil {
		t.Fatal("BootstrapV1(nil) must return an error")
	}
}

// TestApp_Close_Idempotent verifies Close() can be called multiple
// times without re-running closers or returning a stale error.
func TestApp_Close_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	app, err := BootstrapV1(validCfg(dbPath))
	if err != nil {
		t.Fatalf("BootstrapV1: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Errorf("second Close (idempotent): %v", err)
	}
}

// TestApp_Close_Nil: calling Close on nil App is safe.
func TestApp_Close_Nil(t *testing.T) {
	var app *App
	if err := app.Close(); err != nil {
		t.Errorf("nil App.Close should be safe; got %v", err)
	}
}

// TestBootstrapV1_DatabaseDirCreated verifies that a non-existent
// parent directory for DatabasePath is created automatically.
func TestBootstrapV1_DatabaseDirCreated(t *testing.T) {
	// Three levels of non-existent dirs.
	dbPath := filepath.Join(t.TempDir(), "a", "b", "c", "data.db")
	app, err := BootstrapV1(validCfg(dbPath))
	if err != nil {
		t.Fatalf("BootstrapV1 should auto-create parent dirs: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
}

// TestBootstrapV1_PermissionModeProjection verifies that
// config.PermissionMode (string) flows correctly into the gate's
// runtime mode.
func TestBootstrapV1_PermissionModeProjection(t *testing.T) {
	cases := []struct {
		in   config.PermissionMode
		want string
	}{
		{config.ModeDefault, "default"},
		{config.ModeAutoEdit, "auto-edit"},
		{config.ModeYes, "yes"},
		{config.ModePlan, "plan"},
		{"", "default"}, // empty falls back
	}
	for _, c := range cases {
		t.Run(string(c.in), func(t *testing.T) {
			cfg := validCfg(filepath.Join(t.TempDir(), "data.db"))
			cfg.Permission.Mode = c.in
			app, err := BootstrapV1(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.Close() })
			if got := app.Permission.GetMode().String(); got != c.want {
				t.Errorf("mode projection: cfg=%q gate=%q want %q", c.in, got, c.want)
			}
		})
	}
}

// TestBootstrapV1_PermissionMissingRulesFile verifies that the gate
// boots cleanly when the configured rules_file doesn't exist; the
// hard blacklist still loads.
func TestBootstrapV1_PermissionMissingRulesFile(t *testing.T) {
	cfg := validCfg(filepath.Join(t.TempDir(), "data.db"))
	cfg.Permission.RulesFile = filepath.Join(t.TempDir(), "no-such.yaml")

	app, err := BootstrapV1(cfg)
	if err != nil {
		t.Fatalf("missing rules file should be benign; got %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if app.Permission == nil {
		t.Error("Permission gate must still be wired even with missing rules file")
	}
}

// ============================================================
// helpers
// ============================================================

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// equalStringSlices reports whether a and b have the same length and
// the same elements in the same order. We use it for the strict
// tool-roster assertion in TestBootstrapV1_Happy.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
