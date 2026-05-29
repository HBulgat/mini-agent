package llm

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a minimal Provider implementation for Registry
// tests. It returns the supplied name + a Capabilities row whose Model
// is what the test wants to "match" against.
type fakeProvider struct {
	name  string
	model string
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Capabilities() Capabilities {
	return Capabilities{Model: f.model, ContextWindow: 1024}
}
func (f *fakeProvider) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	return nil, errors.New("not implemented")
}

// ---------- SplitModelRef ----------

func TestSplitModelRef(t *testing.T) {
	cases := []struct {
		in           string
		wantProvider string
		wantModel    string
		wantExplicit bool
	}{
		{"deepseek:deepseek-reasoner", "deepseek", "deepseek-reasoner", true},
		{"openai:gpt-4o", "openai", "gpt-4o", true},
		{"claude-sonnet-4-5", "", "claude-sonnet-4-5", false},
		{"  ", "", "", false},
		{"", "", "", false},
		{":bare", "", "bare", true},
	}
	for _, c := range cases {
		p, m, e := SplitModelRef(c.in)
		if p != c.wantProvider || m != c.wantModel || e != c.wantExplicit {
			t.Errorf("SplitModelRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, p, m, e, c.wantProvider, c.wantModel, c.wantExplicit)
		}
	}
}

// ---------- Registry happy paths ----------

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	a := &fakeProvider{name: "a", model: "ma"}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("a")
	if !ok || got != a {
		t.Errorf("Get: got %v, want %v", got, a)
	}
}

func TestRegistry_DuplicateNameRejected(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeProvider{name: "x", model: "m1"})
	err := r.Register(&fakeProvider{name: "x", model: "m2"})
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestRegistry_RejectNilOrEmptyName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Error("expected error for nil provider")
	}
	if err := r.Register(&fakeProvider{name: ""}); err == nil {
		t.Error("expected error for empty Name()")
	}
}

// ---------- GetByModel ----------

func TestRegistry_GetByModel_Explicit(t *testing.T) {
	r := NewRegistry()
	a := &fakeProvider{name: "anthropic", model: "claude-sonnet-4-5"}
	_ = r.Register(a)
	got, err := r.GetByModel("anthropic:claude-sonnet-4-5")
	if err != nil || got != a {
		t.Errorf("explicit ref: got=%v err=%v", got, err)
	}
}

func TestRegistry_GetByModel_PrefixGuess(t *testing.T) {
	r := NewRegistry()
	a := &fakeProvider{name: "anthropic", model: "claude-haiku-4-5"}
	_ = r.Register(a)
	got, err := r.GetByModel("claude-sonnet-4-5") // bare claude-* → anthropic
	if err != nil || got != a {
		t.Errorf("prefix-guess: got=%v err=%v", got, err)
	}
}

func TestRegistry_GetByModel_MatchByCapsModel(t *testing.T) {
	r := NewRegistry()
	a := &fakeProvider{name: "deepseek", model: "deepseek-chat"}
	_ = r.Register(a)
	// Bare "deepseek-chat" — guessProviderByModel maps deepseek-* to "deepseek".
	got, err := r.GetByModel("deepseek-chat")
	if err != nil || got != a {
		t.Errorf("bare match: got=%v err=%v", got, err)
	}
}

func TestRegistry_GetByModel_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.GetByModel("nope")
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("got %v, want ErrProviderNotFound", err)
	}
}

func TestRegistry_GetByModel_Ambiguous(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeProvider{name: "p1", model: "shared-model"})
	_ = r.Register(&fakeProvider{name: "p2", model: "shared-model"})
	_, err := r.GetByModel("shared-model")
	if !errors.Is(err, ErrAmbiguousModelRef) {
		t.Errorf("got %v, want ErrAmbiguousModelRef", err)
	}
}

// ---------- SetActive / Active ----------

func TestRegistry_SetActiveAndActive(t *testing.T) {
	r := NewRegistry()
	a := &fakeProvider{name: "deepseek", model: "deepseek-chat"}
	b := &fakeProvider{name: "openai", model: "gpt-4o"}
	_ = r.Register(a)
	_ = r.Register(b)

	if err := r.SetActive("openai:gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if r.Active() != b {
		t.Errorf("Active: got %v, want %v", r.Active(), b)
	}
}

func TestRegistry_SetActiveEmptyFallsBackToFirst(t *testing.T) {
	r := NewRegistry()
	first := &fakeProvider{name: "first", model: "m"}
	second := &fakeProvider{name: "second", model: "n"}
	_ = r.Register(first)
	_ = r.Register(second)
	if err := r.SetActive(""); err != nil {
		t.Fatal(err)
	}
	if r.Active() != first {
		t.Errorf("Active: got %v, want first", r.Active())
	}
}

func TestRegistry_SetActiveEmptyOnEmpty(t *testing.T) {
	r := NewRegistry()
	if err := r.SetActive(""); err == nil {
		t.Error("expected error when registry is empty")
	}
}

// ---------- List ----------

func TestRegistry_ListPreservesOrder(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"a", "b", "c"} {
		_ = r.Register(&fakeProvider{name: name, model: "m"})
	}
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Name() != want {
			t.Errorf("List[%d] = %s, want %s", i, got[i].Name(), want)
		}
	}
}
