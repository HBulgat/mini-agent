package llm

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Registry is the in-process Provider registry. It maps Provider names
// to instances and tracks which model is currently active. Most
// concrete behavior is wired by internal/bootstrap at startup; the
// agent loop later calls SetActive (`/model` slash command) and Active
// (every turn).
//
// Concurrency: methods are safe for concurrent use — agent goroutines
// may read while a `/model` command writes.
type Registry interface {
	// Register adds a Provider. Returns an error if a Provider with the
	// same Name() already exists. Names are case-sensitive.
	Register(p Provider) error

	// Get fetches a Provider by exact Name(). The bool return
	// distinguishes "not registered" from "registered but nil" (which
	// shouldn't happen in practice).
	Get(name string) (Provider, bool)

	// GetByModel resolves a model reference (`provider:model` or bare
	// model) to a Provider. Bare model names use the heuristic from
	// §8.11.2 (claude-* → anthropic, gpt-*/o*-* → openai, etc.).
	// Ambiguous bare names return an error so callers can surface the
	// fix-it message ("please use provider:model form").
	GetByModel(modelRef string) (Provider, error)

	// List returns every registered Provider in registration order.
	// Bootstrap uses it to print "Loaded providers: [deepseek, openai-real, ...]".
	List() []Provider

	// Active returns the Provider currently selected for new requests.
	// Returns nil when no provider was registered or SetActive hasn't
	// been called.
	Active() Provider

	// SetActive parses modelRef (provider:model or bare model), looks
	// up the matching Provider, and marks it active. Empty modelRef
	// falls back to the first registered Provider.
	SetActive(modelRef string) error
}

// ErrProviderNotFound is returned by Get / GetByModel when no Provider
// matches.
var ErrProviderNotFound = errors.New("llm: provider not found")

// ErrAmbiguousModelRef is returned by GetByModel when a bare model
// name could plausibly belong to more than one registered Provider.
var ErrAmbiguousModelRef = errors.New("llm: model reference ambiguous; use provider:model form")

// NewRegistry creates an empty Registry backed by a map + RWMutex. The
// implementation is concrete (not exported) so callers always go
// through the Registry interface.
func NewRegistry() Registry {
	return &memRegistry{
		providers: make(map[string]Provider),
	}
}

// memRegistry is the in-memory Registry implementation. We keep the
// insertion order in `order` so List() is deterministic — bootstrap
// log lines "loaded providers: a, b, c" should match config order.
type memRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string
	active    Provider
}

func (r *memRegistry) Register(p Provider) error {
	if p == nil {
		return errors.New("llm: register nil provider")
	}
	name := p.Name()
	if name == "" {
		return errors.New("llm: register provider with empty Name()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.providers[name]; dup {
		return fmt.Errorf("llm: provider %q already registered", name)
	}
	r.providers[name] = p
	r.order = append(r.order, name)
	return nil
}

func (r *memRegistry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

func (r *memRegistry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.providers[name])
	}
	return out
}

func (r *memRegistry) Active() Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

func (r *memRegistry) GetByModel(modelRef string) (Provider, error) {
	provider, model, explicit := SplitModelRef(modelRef)

	r.mu.RLock()
	defer r.mu.RUnlock()

	if explicit {
		p, ok := r.providers[provider]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrProviderNotFound, provider)
		}
		_ = model // model is informational at this layer; Provider holds its own active model
		return p, nil
	}

	// Bare model — try the well-known prefix table first.
	if guessed := guessProviderByModel(model); guessed != "" {
		if p, ok := r.providers[guessed]; ok {
			return p, nil
		}
	}

	// Otherwise, check the model against every registered Provider's
	// active Capabilities().Model. Multiple matches → ambiguous.
	var matches []Provider
	for _, name := range r.order {
		p := r.providers[name]
		if strings.EqualFold(p.Capabilities().Model, model) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrProviderNotFound, model)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%w: %q matches %d providers", ErrAmbiguousModelRef, model, len(matches))
	}
}

func (r *memRegistry) SetActive(modelRef string) error {
	if strings.TrimSpace(modelRef) == "" {
		// Fall back to the first registered Provider.
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.order) == 0 {
			return errors.New("llm: SetActive: no providers registered")
		}
		r.active = r.providers[r.order[0]]
		return nil
	}
	p, err := r.GetByModel(modelRef)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.active = p
	r.mu.Unlock()
	return nil
}

// SplitModelRef parses "provider:model" or bare "model".
//   - "deepseek:deepseek-chat" → ("deepseek", "deepseek-chat", true)
//   - "claude-sonnet-4-5"      → ("",         "claude-sonnet-4-5", false)
//   - ""                       → ("",         "",                  false)
//
// We export this so config and `/model` flag parsers can reuse it.
func SplitModelRef(ref string) (provider, model string, explicit bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", false
	}
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		return ref[:i], ref[i+1:], true
	}
	return "", ref, false
}

// guessProviderByModel maps well-known model-name prefixes onto
// Provider names. The list mirrors §8.11.2 — keep it short and let
// the explicit `provider:model` form handle the long tail.
//
// Returns "" when no prefix matches; callers fall through to the
// "match by Capabilities().Model" path.
func guessProviderByModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini-"):
		return "gemini"
	case strings.HasPrefix(m, "deepseek-"):
		return "deepseek"
	}
	return ""
}
