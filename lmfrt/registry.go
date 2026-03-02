package lmfrt

import (
	"fmt"
	"sort"
	"strings"
)

type Registry struct {
	functions map[string]*Function
}

func NewRegistry() *Registry {
	return &Registry{functions: map[string]*Function{}}
}

func (r *Registry) Register(fn *Function) error {
	if fn == nil {
		return fmt.Errorf("function cannot be nil")
	}
	if fn.ID == "" {
		return fmt.Errorf("function ID cannot be empty")
	}
	if strings.TrimSpace(fn.WhenToUse) == "" {
		return fmt.Errorf("function %q must provide non-empty WhenToUse", fn.ID)
	}
	if fn.Exec == nil {
		return fmt.Errorf("function %q has nil Exec", fn.ID)
	}
	if _, exists := r.functions[fn.ID]; exists {
		return fmt.Errorf("function %q is already registered", fn.ID)
	}
	r.functions[fn.ID] = fn
	return nil
}

func (r *Registry) MustRegister(fn *Function) {
	if err := r.Register(fn); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(id string) (*Function, bool) {
	fn, ok := r.functions[id]
	return fn, ok
}

func (r *Registry) List() []Function {
	items := make([]Function, 0, len(r.functions))
	for _, fn := range r.functions {
		items = append(items, *fn)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
