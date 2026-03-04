package semtrace

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type ctxKey string

const (
	taskIDKey   ctxKey = "semtrace_task_id"
	regionIDKey ctxKey = "semtrace_region_id"
)

type Event struct {
	Seq            uint64         `json:"seq"`
	Kind           string         `json:"kind"`
	TsNS           int64          `json:"ts_ns"`
	TaskID         uint64         `json:"task_id,omitempty"`
	ParentTaskID   uint64         `json:"parent_task_id,omitempty"`
	RegionID       uint64         `json:"region_id,omitempty"`
	ParentRegionID uint64         `json:"parent_region_id,omitempty"`
	Name           string         `json:"name,omitempty"`
	Category       string         `json:"category,omitempty"`
	Message        string         `json:"message,omitempty"`
	Attrs          map[string]any `json:"attrs,omitempty"`
}

type Dump struct {
	Version           int     `json:"version"`
	Clock             string  `json:"clock"`
	TimeOriginUnixNS  int64   `json:"time_origin_unix_ns"`
	TimeOriginMonoRef string  `json:"time_origin_monotonic_ref"`
	Events            []Event `json:"events"`
}

type Recorder struct {
	mu         sync.Mutex
	events     []Event
	idCounter  uint64
	seqCounter uint64
	active     atomic.Bool
	startWall  time.Time
	startMono  time.Time
}

type Task struct {
	recorder *Recorder
	id       uint64
	once     sync.Once
}

type Region struct {
	recorder *Recorder
	id       uint64
	once     sync.Once
}

var globalRecorder = NewRecorder()

func Global() *Recorder {
	return globalRecorder
}

func NewRecorder() *Recorder {
	r := &Recorder{}
	r.startWall = time.Now().UTC()
	r.startMono = time.Now()
	return r
}

func (r *Recorder) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = r.events[:0]
	r.idCounter = 0
	r.seqCounter = 0
	r.startWall = time.Now().UTC()
	r.startMono = time.Now()
	r.active.Store(true)
}

func (r *Recorder) StopAndDump() Dump {
	r.active.Store(false)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return Dump{
		Version:           1,
		Clock:             "monotonic_ns_since_origin",
		TimeOriginUnixNS:  r.startWall.UnixNano(),
		TimeOriginMonoRef: "process-local monotonic clock",
		Events:            out,
	}
}

func NewTask(ctx context.Context, name string) (context.Context, *Task) {
	return Global().NewTaskWithAttrs(ctx, name, nil)
}

func StartRegion(ctx context.Context, name string) (context.Context, *Region) {
	return Global().StartRegionWithAttrs(ctx, name, nil)
}

func Logf(ctx context.Context, category string, format string, args ...any) {
	Global().LogAttrs(ctx, category, fmt.Sprintf(format, args...), nil)
}

func NewTaskWithAttrs(ctx context.Context, name string, attrs map[string]any) (context.Context, *Task) {
	return Global().NewTaskWithAttrs(ctx, name, attrs)
}

func StartRegionWithAttrs(ctx context.Context, name string, attrs map[string]any) (context.Context, *Region) {
	return Global().StartRegionWithAttrs(ctx, name, attrs)
}

func LogAttrs(ctx context.Context, category string, message string, attrs map[string]any) {
	Global().LogAttrs(ctx, category, message, attrs)
}

func (r *Recorder) NewTaskWithAttrs(ctx context.Context, name string, attrs map[string]any) (context.Context, *Task) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !r.active.Load() {
		return ctx, &Task{}
	}
	id := r.nextID()
	parentTaskID, _ := currentTaskID(ctx)
	r.append(Event{
		Kind:         "task_start",
		TaskID:       id,
		ParentTaskID: parentTaskID,
		Name:         name,
		Attrs:        cloneAttrs(attrs),
	})
	ctx = context.WithValue(ctx, taskIDKey, id)
	ctx = context.WithValue(ctx, regionIDKey, uint64(0))
	return ctx, &Task{recorder: r, id: id}
}

func (r *Recorder) StartRegionWithAttrs(ctx context.Context, name string, attrs map[string]any) (context.Context, *Region) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !r.active.Load() {
		return ctx, &Region{}
	}
	id := r.nextID()
	taskID, _ := currentTaskID(ctx)
	parentRegionID, _ := currentRegionID(ctx)
	r.append(Event{
		Kind:           "region_start",
		TaskID:         taskID,
		RegionID:       id,
		ParentRegionID: parentRegionID,
		Name:           name,
		Attrs:          cloneAttrs(attrs),
	})
	ctx = context.WithValue(ctx, regionIDKey, id)
	return ctx, &Region{recorder: r, id: id}
}

func (r *Recorder) LogAttrs(ctx context.Context, category string, message string, attrs map[string]any) {
	if !r.active.Load() {
		return
	}
	taskID, _ := currentTaskID(ctx)
	regionID, _ := currentRegionID(ctx)
	r.append(Event{
		Kind:     "log",
		TaskID:   taskID,
		RegionID: regionID,
		Category: category,
		Message:  message,
		Attrs:    cloneAttrs(attrs),
	})
}

func (t *Task) End() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.recorder == nil || t.id == 0 || !t.recorder.active.Load() {
			return
		}
		t.recorder.append(Event{Kind: "task_end", TaskID: t.id})
	})
}

func (r *Region) End() {
	r.EndWithAttrs(nil)
}

func (r *Region) EndWithAttrs(attrs map[string]any) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if r.recorder == nil || r.id == 0 || !r.recorder.active.Load() {
			return
		}
		r.recorder.append(Event{Kind: "region_end", RegionID: r.id, Attrs: cloneAttrs(attrs)})
	})
}

func (r *Recorder) nextID() uint64 {
	return atomic.AddUint64(&r.idCounter, 1)
}

func (r *Recorder) append(ev Event) {
	now := time.Now()
	ev.Seq = atomic.AddUint64(&r.seqCounter, 1)
	ev.TsNS = now.Sub(r.startMono).Nanoseconds()
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func currentTaskID(ctx context.Context) (uint64, bool) {
	if ctx == nil {
		return 0, false
	}
	v := ctx.Value(taskIDKey)
	id, ok := v.(uint64)
	if !ok || id == 0 {
		return 0, false
	}
	return id, true
}

func currentRegionID(ctx context.Context) (uint64, bool) {
	if ctx == nil {
		return 0, false
	}
	v := ctx.Value(regionIDKey)
	id, ok := v.(uint64)
	if !ok || id == 0 {
		return 0, false
	}
	return id, true
}

func cloneAttrs(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
