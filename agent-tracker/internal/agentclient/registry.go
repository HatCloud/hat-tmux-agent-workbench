package agentclient

import "strings"

// Registry holds adapters in detection-fallback order (after @agent_client prefer).
type Registry struct {
	Adapters []Adapter
}

// DefaultRegistry returns the process-global default. Populated as adapters register
// (claude → codex → grok). Starts empty until Task 2+ register.
var defaultAdapters []Adapter

// RegisterDefault appends an adapter to the default list if ID not already present.
func RegisterDefault(a Adapter) {
	if a == nil {
		return
	}
	id := a.ID()
	for _, existing := range defaultAdapters {
		if existing.ID() == id {
			return
		}
	}
	defaultAdapters = append(defaultAdapters, a)
}

// DefaultRegistry returns a Registry snapshot of registered adapters.
func DefaultRegistry() *Registry {
	cp := make([]Adapter, len(defaultAdapters))
	copy(cp, defaultAdapters)
	return &Registry{Adapters: cp}
}

// ResetDefaultForTest clears default adapters (tests only).
func ResetDefaultForTest() {
	defaultAdapters = nil
}

// DetectForPane prefers tagged client when Detect succeeds strictly; otherwise
// walks Adapters in order. Unknown taggedClient is ignored.
func (r *Registry) DetectForPane(idx *Index, panePID int, taggedClient string) (LiveSession, bool) {
	if r == nil || idx == nil || panePID <= 0 {
		return LiveSession{}, false
	}
	tag := strings.TrimSpace(strings.ToLower(taggedClient))
	if tag != "" {
		if a := r.byID(tag); a != nil {
			if s, ok := a.Detect(idx, panePID); ok {
				return s, true
			}
			// strict miss → fallthrough to ordered list
		}
		// unknown tag → fallthrough
	}
	for _, a := range r.Adapters {
		if s, ok := a.Detect(idx, panePID); ok {
			return s, true
		}
	}
	return LiveSession{}, false
}

func (r *Registry) byID(id string) Adapter {
	for _, a := range r.Adapters {
		if a != nil && a.ID() == id {
			return a
		}
	}
	return nil
}

// IDs returns adapter IDs in order.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Adapters))
	for _, a := range r.Adapters {
		if a != nil {
			out = append(out, a.ID())
		}
	}
	return out
}
