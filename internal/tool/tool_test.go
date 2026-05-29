package tool

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// ============================================================
// Category & Mode enum strings
// ============================================================

func TestCategory_String(t *testing.T) {
	cases := []struct {
		c    Category
		want string
	}{
		{CategoryReadOnly, "read_only"},
		{CategoryWrite, "write"},
		{CategoryExecute, "execute"},
		{CategoryNetwork, "network"},
		{CategoryMeta, "meta"},
		{Category(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.c.String(); got != c.want {
			t.Errorf("Category(%d).String() = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestMode_String(t *testing.T) {
	cases := []struct {
		m    Mode
		want string
	}{
		{ModeDefault, "default"},
		{ModeAutoEdit, "auto-edit"},
		{ModeYes, "yes"},
		{ModePlan, "plan"},
		{Mode(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.m.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", c.m, got, c.want)
		}
	}
}

// ============================================================
// IsCode / MapIOError / MapCtxError
// ============================================================

func TestIsCode(t *testing.T) {
	te := &Error{Code: ErrInvalidArgs, Message: "bad"}
	if !IsCode(te, ErrInvalidArgs) {
		t.Error("IsCode should match own code")
	}
	if IsCode(te, ErrIO) {
		t.Error("IsCode should not match different code")
	}
	if IsCode(errors.New("plain"), ErrInvalidArgs) {
		t.Error("IsCode on non-tool.Error should be false")
	}
	if IsCode(nil, ErrInvalidArgs) {
		t.Error("IsCode(nil) should be false")
	}
}

func TestMapIOError(t *testing.T) {
	if MapIOError(nil) != nil {
		t.Error("MapIOError(nil) must be nil")
	}
	notFound := MapIOError(fs.ErrNotExist)
	if !IsCode(notFound, ErrNotFound) {
		t.Errorf("ErrNotExist should map to ErrNotFound, got %v", notFound)
	}
	io := MapIOError(errors.New("disk on fire"))
	if !IsCode(io, ErrIO) {
		t.Errorf("generic error should map to ErrIO, got %v", io)
	}
}

func TestMapCtxError(t *testing.T) {
	if MapCtxError(nil) != nil {
		t.Error("MapCtxError(nil) must be nil")
	}
	if !IsCode(MapCtxError(context.Canceled), ErrInterrupted) {
		t.Error("Canceled should map to ErrInterrupted")
	}
	if !IsCode(MapCtxError(context.DeadlineExceeded), ErrTimeout) {
		t.Error("DeadlineExceeded should map to ErrTimeout")
	}
	other := errors.New("not ctx")
	if got := MapCtxError(other); got != other {
		t.Error("non-ctx error should pass through unchanged")
	}
}

// ============================================================
// Registry
// ============================================================

// fakeTool is a minimal Tool for registry tests. We don't reuse the
// real read_file impl here because the registry tests should not
// pull in fs, jsonschema, or any concrete behaviour.
type fakeTool struct {
	name        string
	description string
	schema      map[string]any
	cat         Category
}

func (f *fakeTool) Name() string                                                  { return f.name }
func (f *fakeTool) Description() string                                           { return f.description }
func (f *fakeTool) Schema() map[string]any                                        { return f.schema }
func (f *fakeTool) Category() Category                                            { return f.cat }
func (f *fakeTool) Invoke(ctx context.Context, _ map[string]any) (Result, error) { return Result{}, nil }

func goodTool(name string, cat Category) *fakeTool {
	return &fakeTool{
		name:        name,
		description: "a test tool",
		schema:      map[string]any{"type": "object"},
		cat:         cat,
	}
}

func TestRegistry_Register_Validation(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) should fail")
	}

	cases := []struct {
		name string
		tool *fakeTool
	}{
		{"empty name", &fakeTool{description: "x", schema: map[string]any{"type": "object"}}},
		{"empty description", &fakeTool{name: "x", schema: map[string]any{"type": "object"}}},
		{"nil schema", &fakeTool{name: "x", description: "x"}},
		{"schema missing type", &fakeTool{name: "x", description: "x", schema: map[string]any{"foo": 1}}},
	}
	for _, c := range cases {
		if err := r.Register(c.tool); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}

	// Happy path
	if err := r.Register(goodTool("t1", CategoryReadOnly)); err != nil {
		t.Fatalf("happy register failed: %v", err)
	}
	// Duplicate
	if err := r.Register(goodTool("t1", CategoryReadOnly)); err == nil {
		t.Error("duplicate name should fail")
	}
}

func TestRegistry_GetListSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zeta", "alpha", "mu"} {
		_ = r.Register(goodTool(n, CategoryReadOnly))
	}
	tt, ok := r.Get("alpha")
	if !ok || tt.Name() != "alpha" {
		t.Error("Get(alpha) failed")
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get(missing) should not return ok")
	}
	all := r.List()
	got := []string{all[0].Name(), all[1].Name(), all[2].Name()}
	want := []string{"alpha", "mu", "zeta"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("List() not sorted: got %v want %v", got, want)
		}
	}
}

func TestRegistry_ListAvailable_PlanMode(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(goodTool("read", CategoryReadOnly))
	_ = r.Register(goodTool("write", CategoryWrite))
	_ = r.Register(goodTool("exec", CategoryExecute))
	_ = r.Register(goodTool("net", CategoryNetwork))
	_ = r.Register(goodTool("meta", CategoryMeta))

	// Non-plan modes: see everything.
	for _, m := range []Mode{ModeDefault, ModeAutoEdit, ModeYes} {
		if got := len(r.ListAvailable(m)); got != 5 {
			t.Errorf("mode=%v ListAvailable len=%d, want 5", m, got)
		}
	}

	// Plan mode: only read-only + meta.
	avail := r.ListAvailable(ModePlan)
	if len(avail) != 2 {
		t.Fatalf("plan ListAvailable len=%d, want 2 (read, meta); got %v",
			len(avail), names(avail))
	}
	gotNames := names(avail)
	if !contains(gotNames, "read") || !contains(gotNames, "meta") {
		t.Errorf("plan ListAvailable should contain read+meta, got %v", gotNames)
	}
}

func TestRegistry_ToSpecs(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(goodTool("read", CategoryReadOnly))
	_ = r.Register(goodTool("write", CategoryWrite))

	specs := r.ToSpecs(ModeDefault)
	if len(specs) != 2 {
		t.Fatalf("ToSpecs len=%d, want 2", len(specs))
	}
	for _, s := range specs {
		if s.Name == "" || s.Description == "" || s.Schema == nil {
			t.Errorf("incomplete ToolSpec: %+v", s)
		}
	}

	// Plan mode should drop the write tool.
	planSpecs := r.ToSpecs(ModePlan)
	if len(planSpecs) != 1 || planSpecs[0].Name != "read" {
		t.Errorf("plan ToSpecs = %v, want [read]", planSpecs)
	}

	// Compile-time sanity: ToSpecs returns []llm.ToolSpec.
	var _ []llm.ToolSpec = specs
}

// helpers

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}

func contains(slice []string, x string) bool {
	for _, s := range slice {
		if s == x {
			return true
		}
	}
	return false
}
