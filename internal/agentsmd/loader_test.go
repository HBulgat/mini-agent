package agentsmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny helper for arranging test files. Centralised so
// the assertions below can stay focused on the loader's behaviour.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// loadOK runs Load and fails the test on a non-nil error. Returns
// the merged text.
func loadOK(t *testing.T, l Loader, cwd string) string {
	t.Helper()
	got, err := l.Load(context.Background(), cwd)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return got
}

// =====================================================================
// Discovery
// =====================================================================

func TestLoad_NeitherSource_ReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	l := New(&Config{
		GlobalPath:    filepath.Join(tmp, "nope.md"),
		ProjectLookup: true,
	})
	if got := loadOK(t, l, tmp); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestLoad_GlobalOnly(t *testing.T) {
	tmp := t.TempDir()
	gpath := filepath.Join(tmp, "global.md")
	writeFile(t, gpath, "global rules\n")

	l := New(&Config{GlobalPath: gpath, ProjectLookup: true})
	got := loadOK(t, l, tmp) // tmp has no AGENTS.md
	if got != "global rules" {
		t.Errorf("expected %q, got %q", "global rules", got)
	}
}

func TestLoad_ProjectOnly(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "project rules\n")

	l := New(&Config{
		// No global path configured.
		ProjectLookup: true,
	})
	got := loadOK(t, l, tmp)
	if got != "project rules" {
		t.Errorf("expected %q, got %q", "project rules", got)
	}
}

func TestLoad_BothSources_GlobalFirstThenProject(t *testing.T) {
	tmp := t.TempDir()
	gpath := filepath.Join(tmp, "global.md")
	writeFile(t, gpath, "GLOBAL")
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "PROJECT")

	l := New(&Config{GlobalPath: gpath, ProjectLookup: true})
	got := loadOK(t, l, tmp)

	want := "GLOBAL\n\n---\n\nPROJECT"
	if got != want {
		t.Errorf("merge order/separator wrong:\n got: %q\nwant: %q", got, want)
	}
}

func TestLoad_NoUpwardRecursion(t *testing.T) {
	// AGENTS.md placed in parent dir must NOT be discovered when
	// the loader is invoked with the child as cwd. This is the
	// monorepo-safety guarantee from D27.
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "AGENTS.md"), "ANCESTOR")
	child := filepath.Join(parent, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	l := New(&Config{ProjectLookup: true})
	if got := loadOK(t, l, child); got != "" {
		t.Errorf("expected no upward recursion; got %q", got)
	}
}

func TestLoad_ProjectLookupDisabled(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "PROJECT")

	l := New(&Config{ProjectLookup: false})
	if got := loadOK(t, l, tmp); got != "" {
		t.Errorf("expected project lookup disabled; got %q", got)
	}
}

// =====================================================================
// Empty / unreadable
// =====================================================================

func TestLoad_EmptyFileTreatedAsMissing(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "")

	l := New(&Config{ProjectLookup: true})
	if got := loadOK(t, l, tmp); got != "" {
		t.Errorf("expected empty file → missing; got %q", got)
	}
}

func TestLoad_WhitespaceOnlyFileTreatedAsMissing(t *testing.T) {
	// "   \n\n\t" should not produce a stray "---" separator when
	// merged with a global file.
	tmp := t.TempDir()
	gpath := filepath.Join(tmp, "global.md")
	writeFile(t, gpath, "GLOBAL")
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "   \n\n\t   \n")

	l := New(&Config{GlobalPath: gpath, ProjectLookup: true})
	got := loadOK(t, l, tmp)
	if got != "GLOBAL" {
		t.Errorf("whitespace-only project file should be elided; got %q", got)
	}
}

func TestLoad_UnreadableFileTreatedAsMissing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, chmod-based unreadability test is meaningless")
	}
	tmp := t.TempDir()
	apath := filepath.Join(tmp, "AGENTS.md")
	writeFile(t, apath, "secret rules")
	if err := os.Chmod(apath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Ensure t.TempDir cleanup can remove the file.
		_ = os.Chmod(apath, 0o644)
	})

	l := New(&Config{ProjectLookup: true})
	if got := loadOK(t, l, tmp); got != "" {
		t.Errorf("expected unreadable file to be elided; got %q", got)
	}
}

func TestLoad_DirectoryNamedAGENTSmd(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := New(&Config{ProjectLookup: true})
	if got := loadOK(t, l, tmp); got != "" {
		t.Errorf("AGENTS.md as directory should be skipped; got %q", got)
	}
}

// =====================================================================
// Truncation
// =====================================================================

func TestLoad_OversizeFileTruncated(t *testing.T) {
	tmp := t.TempDir()
	// 2 KiB body, cap at 256 bytes — easy to read assertions, no
	// risk of accidentally hitting the default 1 MiB cap.
	body := strings.Repeat("a", 2048)
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), body)

	l := New(&Config{ProjectLookup: true, MaxBytes: 256})
	got := loadOK(t, l, tmp)

	// Must contain exactly the first 256 chars + truncation marker.
	if len(got) < 256 {
		t.Fatalf("expected at least 256 bytes preserved; got %d", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 256)) {
		t.Errorf("expected leading 256 'a' chars; got %q", got[:min(60, len(got))])
	}
	if !strings.Contains(got, "[...truncated, 2048 bytes total]") {
		t.Errorf("missing truncation marker:\n%s", got)
	}
}

func TestLoad_FileExactlyAtCap_NotTruncated(t *testing.T) {
	tmp := t.TempDir()
	body := strings.Repeat("b", 100)
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), body)

	l := New(&Config{ProjectLookup: true, MaxBytes: 100})
	got := loadOK(t, l, tmp)

	if got != body {
		t.Errorf("expected file at cap to load as-is; got %q (len=%d)", got, len(got))
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("file at cap should NOT be marked truncated; got %q", got)
	}
}

// =====================================================================
// Construction defaults
// =====================================================================

func TestNew_NilConfig(t *testing.T) {
	l := New(nil)
	got, err := l.Load(context.Background(), "/")
	if err != nil {
		t.Fatalf("nil cfg should not error; got %v", err)
	}
	if got != "" {
		t.Errorf("nil cfg should produce no guidance; got %q", got)
	}
}

func TestNew_DefaultMaxBytes(t *testing.T) {
	l := New(&Config{ProjectLookup: true})
	rl, ok := l.(*realLoader)
	if !ok {
		t.Fatalf("New returned %T, expected *realLoader", l)
	}
	if rl.maxBytes != DefaultMaxBytes {
		t.Errorf("default maxBytes wrong: got %d, want %d", rl.maxBytes, DefaultMaxBytes)
	}
}

func TestNew_NegativeMaxBytesUsesDefault(t *testing.T) {
	l := New(&Config{ProjectLookup: true, MaxBytes: -1})
	rl := l.(*realLoader)
	if rl.maxBytes != DefaultMaxBytes {
		t.Errorf("negative maxBytes should fall back to default; got %d", rl.maxBytes)
	}
}

// =====================================================================
// Context cancellation
// =====================================================================

func TestLoad_ContextCancelled(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "ignore me")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := New(&Config{ProjectLookup: true})
	_, err := l.Load(ctx, tmp)
	if err == nil {
		t.Error("expected ctx.Err() from cancelled load")
	}
}

// =====================================================================
// Tilde expansion
// =====================================================================

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"~", home},
		{"~/", home}, // filepath.Join trims trailing /
		{"~/foo", filepath.Join(home, "foo")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~tilde-prefix-only-not-home", "~tilde-prefix-only-not-home"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := expandHome(c.in)
			// "~/" → filepath.Join(home, "") = home; both are
			// acceptable so we normalise for comparison.
			if c.in == "~/" {
				if got != home {
					t.Errorf("got %q want %q", got, home)
				}
				return
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// =====================================================================
// merge logic
// =====================================================================

func TestMergeSections(t *testing.T) {
	cases := []struct {
		name     string
		g, p     string
		want     string
	}{
		{"both empty", "", "", ""},
		{"only global", "G", "", "G"},
		{"only project", "", "P", "P"},
		{"both", "G", "P", "G\n\n---\n\nP"},
		{"both trimmed", "  G \n", " \n P  ", "G\n\n---\n\nP"},
		{"global whitespace only", "  \n\t", "P", "P"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergeSections(c.g, c.p); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// =====================================================================
// "/cd-style" reload semantics — same Loader, different cwd
// =====================================================================

// /cd swaps the project-level source. Since the loader does not
// cache, calling Load with a new cwd must observe the new file.
func TestLoad_CwdChangeReloadsProject(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeFile(t, filepath.Join(a, "AGENTS.md"), "A-RULES")
	writeFile(t, filepath.Join(b, "AGENTS.md"), "B-RULES")

	l := New(&Config{ProjectLookup: true})
	if got := loadOK(t, l, a); got != "A-RULES" {
		t.Errorf("first cwd: got %q want %q", got, "A-RULES")
	}
	if got := loadOK(t, l, b); got != "B-RULES" {
		t.Errorf("after /cd: got %q want %q", got, "B-RULES")
	}
}

// =====================================================================
// Compile-time interface conformance
// =====================================================================

// realLoader must satisfy Loader.
var _ Loader = (*realLoader)(nil)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
