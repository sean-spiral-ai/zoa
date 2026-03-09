package runtime

import (
	"fmt"
	"sort"
	"strings"
)

type registryEntryKind string

const (
	registryEntryFunction registryEntryKind = "function"
	registryEntryMixin    registryEntryKind = "mixin"
)

type registryEntry struct {
	kind     registryEntryKind
	function *Function
	mixin    *Mixin
}

type Registry struct {
	entries map[string]registryEntry
}

func NewRegistry() *Registry {
	return &Registry{entries: map[string]registryEntry{}}
}

func (r *Registry) Register(fn *Function) error {
	if fn == nil {
		return fmt.Errorf("function cannot be nil")
	}
	if strings.TrimSpace(fn.ID) == "" {
		return fmt.Errorf("function ID cannot be empty")
	}
	if strings.TrimSpace(fn.WhenToUse) == "" {
		return fmt.Errorf("function %q must provide non-empty WhenToUse", fn.ID)
	}
	if fn.Exec == nil {
		return fmt.Errorf("function %q has nil Exec", fn.ID)
	}
	if _, exists := r.entries[fn.ID]; exists {
		return fmt.Errorf("id %q is already registered", fn.ID)
	}
	r.entries[fn.ID] = registryEntry{kind: registryEntryFunction, function: fn}
	return nil
}

func (r *Registry) MustRegister(fn *Function) {
	if err := r.Register(fn); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(id string) (*Function, bool) {
	entry, ok := r.entries[id]
	if !ok || entry.kind != registryEntryFunction || entry.function == nil {
		return nil, false
	}
	return entry.function, true
}

func (r *Registry) List() []Function {
	items := make([]Function, 0, len(r.entries))
	for _, entry := range r.entries {
		if entry.kind != registryEntryFunction || entry.function == nil {
			continue
		}
		items = append(items, *entry.function)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (r *Registry) RegisterMixin(mixin *Mixin) error {
	if mixin == nil {
		return fmt.Errorf("mixin cannot be nil")
	}
	if strings.TrimSpace(mixin.ID) == "" {
		return fmt.Errorf("mixin ID cannot be empty")
	}
	if strings.TrimSpace(mixin.WhenToUse) == "" {
		return fmt.Errorf("mixin %q must provide non-empty WhenToUse", mixin.ID)
	}
	if strings.TrimSpace(mixin.Content) == "" {
		return fmt.Errorf("mixin %q must provide non-empty Content", mixin.ID)
	}
	if _, exists := r.entries[mixin.ID]; exists {
		return fmt.Errorf("id %q is already registered", mixin.ID)
	}
	r.entries[mixin.ID] = registryEntry{kind: registryEntryMixin, mixin: mixin}
	return nil
}

func (r *Registry) MustRegisterMixin(mixin *Mixin) {
	if err := r.RegisterMixin(mixin); err != nil {
		panic(err)
	}
}

func (r *Registry) GetMixin(id string) (*Mixin, bool) {
	entry, ok := r.entries[id]
	if !ok || entry.kind != registryEntryMixin || entry.mixin == nil {
		return nil, false
	}
	return entry.mixin, true
}

func (r *Registry) ListMixins() []Mixin {
	items := make([]Mixin, 0, len(r.entries))
	for _, entry := range r.entries {
		if entry.kind != registryEntryMixin || entry.mixin == nil {
			continue
		}
		items = append(items, *entry.mixin)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
