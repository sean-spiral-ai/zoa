package main

import (
	"context"
	"log/slog"
)

// debugComponentFilterHandler keeps normal min-level behavior for non-DEBUG logs,
// and allows DEBUG logs only when component=<debugComponent>.
type debugComponentFilterHandler struct {
	next           slog.Handler
	minLevel       slog.Level
	debugComponent string
}

func newDebugComponentFilterHandler(next slog.Handler, minLevel slog.Level, debugComponent string) slog.Handler {
	return &debugComponentFilterHandler{
		next:           next,
		minLevel:       minLevel,
		debugComponent: debugComponent,
	}
}

func (h *debugComponentFilterHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel || level == slog.LevelDebug
}

func (h *debugComponentFilterHandler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level == slog.LevelDebug {
		if !recordHasComponent(rec, h.debugComponent) {
			return nil
		}
		return h.next.Handle(ctx, rec)
	}
	if rec.Level < h.minLevel {
		return nil
	}
	return h.next.Handle(ctx, rec)
}

func (h *debugComponentFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &debugComponentFilterHandler{
		next:           h.next.WithAttrs(attrs),
		minLevel:       h.minLevel,
		debugComponent: h.debugComponent,
	}
}

func (h *debugComponentFilterHandler) WithGroup(name string) slog.Handler {
	return &debugComponentFilterHandler{
		next:           h.next.WithGroup(name),
		minLevel:       h.minLevel,
		debugComponent: h.debugComponent,
	}
}

func recordHasComponent(rec slog.Record, want string) bool {
	matched := false
	rec.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "component" && attr.Value.Kind() == slog.KindString && attr.Value.String() == want {
			matched = true
			return false
		}
		return true
	})
	return matched
}
