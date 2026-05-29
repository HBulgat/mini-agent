package tool

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// Registry is the in-memory catalogue of tools available to the agent.
// It owns:
//   - Lookup by Name() (the LLM's tool_use.name).
//   - Mode-aware filtering for ListAvailable / ToSpecs (e.g. plan mode
//     hides Write/Execute/Network).
//   - Startup-time schema validation (D84) so a malformed tool can't
//     poison the agent loop at request time.
//
// The interface is small on purpose. Permission gating is NOT a
// registry concern — the agent loop calls permission.Gate.Check on
// every tool invocation, regardless of registry filtering.
type Registry interface {
	// Register adds a tool to the catalogue. Returns an error if:
	//   - Name() is empty
	//   - Description() is empty
	//   - Schema() returns nil or a map without a "type" key (D84)
	//   - the name is already taken
	// Should be called only during bootstrap; concurrent Register +
	// Get on a hot loop is supported but unusual.
	Register(t Tool) error

	// Get returns the tool registered under name, or (nil, false) if
	// no such tool exists.
	Get(name string) (Tool, bool)

	// List returns every registered tool, sorted by Name() for
	// deterministic output (matters for the system-prompt skill list
	// and for golden-file tests).
	List() []Tool

	// ListAvailable returns the subset of List() that the given mode
	// allows. The mode×category table is in docs/system-design/04-tool-catalog.md.
	ListAvailable(mode Mode) []Tool

	// ToSpecs marshals ListAvailable(mode) into the canonical
	// llm.ToolSpec shape sent to the provider. Used by the agent loop
	// when building each Request.
	ToSpecs(mode Mode) []llm.ToolSpec
}

// memRegistry is the concurrent-safe in-memory implementation. The
// agent only has one Registry per process, but tests need fresh ones,
// so we expose NewRegistry rather than a package-global.
type memRegistry struct {
	mu     sync.RWMutex
	byName map[string]Tool
}

// NewRegistry returns an empty in-memory Registry ready for Register
// calls. Per D31 we return the interface so callers can swap in a
// fake during tests without depending on the concrete struct.
func NewRegistry() Registry {
	return &memRegistry{byName: make(map[string]Tool)}
}

// Register validates the tool's metadata (D84) and stores it. Re-
// registering the same name is rejected — the bootstrap code should
// fail loudly, not silently overwrite.
func (r *memRegistry) Register(t Tool) error {
	if t == nil {
		return errors.New("tool: register nil tool")
	}
	name := t.Name()
	if name == "" {
		return errors.New("tool: empty name")
	}
	if t.Description() == "" {
		return fmt.Errorf("tool %q: empty description", name)
	}
	schema := t.Schema()
	if schema == nil {
		return fmt.Errorf("tool %q: nil schema", name)
	}
	if _, ok := schema["type"]; !ok {
		return fmt.Errorf("tool %q: schema missing 'type' field", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("tool %q: name conflict (already registered)", name)
	}
	r.byName[name] = t
	return nil
}

// Get is the hot-path lookup the agent loop uses on every tool_use.
// We hold only a read lock to allow concurrent reads.
func (r *memRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// List returns a sorted snapshot. The slice is a fresh copy — callers
// can mutate it without affecting the registry.
func (r *memRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.byName))
	for _, t := range r.byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// ListAvailable applies the mode×category policy:
//
//	Mode \ Cat   ReadOnly  Write  Execute  Network  Meta
//	default      ✓         ✓      ✓        ✓        ✓     (writes/exec/net pop approval)
//	auto-edit    ✓         ✓      ✓        ✓        ✓     (writes auto-approved)
//	yes          ✓         ✓      ✓        ✓        ✓     (everything auto except hard blacklist)
//	plan         ✓         ✗      ✗        ✗        ✓     (read-only + meta only)
//
// Per the catalog doc (§4): the registry only filters in plan mode;
// approval gating in default mode is the agent loop's job, not the
// registry's. This keeps the registry stateless w.r.t. user choices.
func (r *memRegistry) ListAvailable(mode Mode) []Tool {
	all := r.List()
	if mode != ModePlan {
		return all
	}
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		c := t.Category()
		if c == CategoryReadOnly || c == CategoryMeta {
			out = append(out, t)
		}
	}
	return out
}

// ToSpecs walks ListAvailable(mode) and serializes each tool's schema
// into the wire shape the provider expects. The agent loop calls this
// per-request; on hot paths we may add a cache, but for now the
// schema reflection cost is negligible (microseconds for a handful of
// tools).
func (r *memRegistry) ToSpecs(mode Mode) []llm.ToolSpec {
	avail := r.ListAvailable(mode)
	specs := make([]llm.ToolSpec, 0, len(avail))
	for _, t := range avail {
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return specs
}
