package tracecontrol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	runtimetrace "runtime/trace"
	"strings"
	"sync"
	"time"

	"zoa/internal/semtrace"
)

type Manager struct {
	mu        sync.Mutex
	active    bool
	startedAt time.Time
	tracePath string
	traceFile *os.File
}

type TraceStatus struct {
	Active    bool      `json:"active"`
	StartedAt time.Time `json:"started_at,omitempty"`
	TracePath string    `json:"trace_path,omitempty"`
}

type traceResponse struct {
	OK        bool      `json:"ok"`
	Message   string    `json:"message,omitempty"`
	Active    bool      `json:"active,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	TracePath string    `json:"trace_path,omitempty"`
}

type endTraceResponse struct {
	OK            bool          `json:"ok"`
	Message       string        `json:"message,omitempty"`
	Active        bool          `json:"active,omitempty"`
	StartedAt     time.Time     `json:"started_at,omitempty"`
	TracePath     string        `json:"trace_path,omitempty"`
	GoTrace       string        `json:"go_trace,omitempty"`
	SemanticTrace semtrace.Dump `json:"semantic_trace"`
}

type EndTraceResult struct {
	Status        TraceStatus
	GoTrace       []byte
	SemanticTrace semtrace.Dump
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) Start() (TraceStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active {
		return TraceStatus{
			Active:    true,
			StartedAt: m.startedAt,
			TracePath: m.tracePath,
		}, fmt.Errorf("trace already running")
	}

	startedAt := time.Now().UTC()
	name := fmt.Sprintf("zoa-runtime-trace-%s.out", startedAt.Format("20060102T150405"))
	path := filepath.Join(os.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		return TraceStatus{}, err
	}
	if err := runtimetrace.Start(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return TraceStatus{}, err
	}
	semtrace.Global().Start()
	m.active = true
	m.startedAt = startedAt
	m.tracePath = path
	m.traceFile = f
	return TraceStatus{
		Active:    true,
		StartedAt: m.startedAt,
		TracePath: m.tracePath,
	}, nil
}

func (m *Manager) Stop() (TraceStatus, []byte, semtrace.Dump, error) {
	m.mu.Lock()
	if !m.active {
		m.mu.Unlock()
		return TraceStatus{}, nil, semtrace.Dump{}, fmt.Errorf("trace is not running")
	}
	path := m.tracePath
	startedAt := m.startedAt
	f := m.traceFile
	m.active = false
	m.tracePath = ""
	m.startedAt = time.Time{}
	m.traceFile = nil
	m.mu.Unlock()

	runtimetrace.Stop()
	if f != nil {
		_ = f.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return TraceStatus{}, nil, semtrace.Dump{}, err
	}
	semantic := semtrace.Global().StopAndDump()
	return TraceStatus{
		Active:    false,
		StartedAt: startedAt,
		TracePath: path,
	}, data, semantic, nil
}

func (m *Manager) Status() TraceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return TraceStatus{
		Active:    m.active,
		StartedAt: m.startedAt,
		TracePath: m.tracePath,
	}
}

func NewHTTPHandler(manager *Manager, logger *slog.Logger) http.Handler {
	if manager == nil {
		manager = NewManager()
	}
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/start_trace", func(w http.ResponseWriter, r *http.Request) {
		if !isMethodAllowed(r.Method) {
			writeMethodNotAllowed(w)
			return
		}
		status, err := manager.Start()
		if err != nil {
			writeJSON(w, http.StatusConflict, traceResponse{
				OK:        false,
				Message:   err.Error(),
				Active:    status.Active,
				StartedAt: status.StartedAt,
				TracePath: status.TracePath,
			})
			return
		}
		logger.Info("runtime trace started", "path", status.TracePath, "started_at", status.StartedAt)
		writeJSON(w, http.StatusOK, traceResponse{
			OK:        true,
			Message:   "trace started",
			Active:    status.Active,
			StartedAt: status.StartedAt,
			TracePath: status.TracePath,
		})
	})
	mux.HandleFunc("/end_trace", func(w http.ResponseWriter, r *http.Request) {
		if !isMethodAllowed(r.Method) {
			writeMethodNotAllowed(w)
			return
		}
		status, goTraceBytes, semanticDump, err := manager.Stop()
		if err != nil {
			writeJSON(w, http.StatusConflict, traceResponse{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		logger.Info("runtime trace stopped", "path", status.TracePath, "go_trace_bytes", len(goTraceBytes), "semantic_events", len(semanticDump.Events))
		writeEndTraceJSON(w, http.StatusOK, endTraceResponse{
			OK:            true,
			Message:       "trace stopped",
			Active:        false,
			StartedAt:     status.StartedAt,
			TracePath:     status.TracePath,
			GoTrace:       base64.StdEncoding.EncodeToString(goTraceBytes),
			SemanticTrace: semanticDump,
		})
	})
	mux.HandleFunc("/trace_status", func(w http.ResponseWriter, r *http.Request) {
		if !isMethodAllowed(r.Method) {
			writeMethodNotAllowed(w)
			return
		}
		status := manager.Status()
		writeJSON(w, http.StatusOK, traceResponse{
			OK:        true,
			Active:    status.Active,
			StartedAt: status.StartedAt,
			TracePath: status.TracePath,
		})
	})

	return loggingMiddleware(logger, mux)
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Debug("trace control request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

func isMethodAllowed(method string) bool {
	return method == http.MethodGet || method == http.MethodPost
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, traceResponse{
		OK:      false,
		Message: "method not allowed",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload traceResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(payload)
}

func writeEndTraceJSON(w http.ResponseWriter, status int, payload endTraceResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(payload)
}

func DownloadTrace(baseURL string) (EndTraceResult, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return EndTraceResult{}, fmt.Errorf("trace control base URL is empty")
	}
	url := strings.TrimRight(baseURL, "/") + "/end_trace"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return EndTraceResult{}, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return EndTraceResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return EndTraceResult{}, err
	}
	var payload endTraceResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return EndTraceResult{}, err
	}
	if resp.StatusCode >= 300 || !payload.OK {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return EndTraceResult{}, errors.New(msg)
	}
	goTraceBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.GoTrace))
	if err != nil {
		return EndTraceResult{}, fmt.Errorf("decode go_trace: %w", err)
	}
	return EndTraceResult{
		Status: TraceStatus{
			TracePath: strings.TrimSpace(payload.TracePath),
			StartedAt: payload.StartedAt,
			Active:    payload.Active,
		},
		GoTrace:       goTraceBytes,
		SemanticTrace: payload.SemanticTrace,
	}, nil
}

func StartTrace(baseURL string) (TraceStatus, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return TraceStatus{}, fmt.Errorf("trace control base URL is empty")
	}
	url := strings.TrimRight(baseURL, "/") + "/start_trace"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return TraceStatus{}, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return TraceStatus{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return TraceStatus{}, err
	}
	var payload traceResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return TraceStatus{}, err
	}
	if resp.StatusCode >= 300 || !payload.OK {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return TraceStatus{}, errors.New(msg)
	}
	return TraceStatus{
		Active:    payload.Active,
		StartedAt: payload.StartedAt,
		TracePath: payload.TracePath,
	}, nil
}
