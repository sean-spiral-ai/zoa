package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"zoa/internal/semtrace"
	"zoa/internal/tracecontrol"
)

const defaultTraceHTTPAddr = "127.0.0.1:3008"
const defaultOmitHiddenRootBelowMS = 75.0

func runTrace(args []string) int {
	traceFlags := flag.NewFlagSet("trace", flag.ContinueOnError)
	traceFlags.SetOutput(os.Stderr)

	var (
		durationSec           float64
		traceHTTPAddr         string
		serveHTTPAddr         string
		waitForEndPrompt      bool
		omitHiddenRootBelowMS float64
	)
	traceFlags.Float64Var(&durationSec, "duration-sec", 3.7, "Trace duration in seconds")
	traceFlags.StringVar(&traceHTTPAddr, "trace-http-addr", defaultTraceHTTPAddr, "Trace control HTTP address or URL")
	traceFlags.StringVar(&serveHTTPAddr, "serve-http-addr", "127.0.0.1:9001", "trace_processor HTTP listen address")
	traceFlags.BoolVar(&waitForEndPrompt, "wait-for-end", true, "Wait for '/end' on stdin instead of stopping after --duration-sec")
	traceFlags.Float64Var(&omitHiddenRootBelowMS, "omit-hidden-root-below-ms", defaultOmitHiddenRootBelowMS, "Drop entire hidden root task trees from semantic trace when root duration is below this threshold (<=0 disables)")
	if err := traceFlags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if traceFlags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional args: %s\n", strings.Join(traceFlags.Args(), " "))
		return 2
	}
	if durationSec <= 0 && !waitForEndPrompt {
		fmt.Fprintln(os.Stderr, "error: --duration-sec must be > 0")
		return 2
	}

	baseURL, err := normalizeTraceControlURL(traceHTTPAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid --trace-http-addr: %v\n", err)
		return 2
	}
	serveHTTPAddr = strings.TrimSpace(serveHTTPAddr)
	if serveHTTPAddr == "" {
		fmt.Fprintln(os.Stderr, "error: --serve-http-addr cannot be empty")
		return 2
	}

	if waitForEndPrompt {
		fmt.Printf("Starting runtime trace via %s (interactive stop)\n", baseURL)
	} else {
		duration := time.Duration(durationSec * float64(time.Second))
		fmt.Printf("Starting runtime trace via %s for %.3fs\n", baseURL, duration.Seconds())
	}
	started, err := tracecontrol.StartTrace(baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: start trace: %v\n", err)
		return 1
	}
	if strings.TrimSpace(started.TracePath) != "" {
		fmt.Printf("Trace started (source path: %s)\n", started.TracePath)
	}

	if waitForEndPrompt {
		if err := waitForEndCommand(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	} else {
		duration := time.Duration(durationSec * float64(time.Second))
		time.Sleep(duration)
	}

	endResult, err := tracecontrol.DownloadTrace(baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: stop trace: %v\n", err)
		return 1
	}

	tracePath := filepath.Join(os.TempDir(), fmt.Sprintf("zoa-runtime-trace-%s.out", time.Now().UTC().Format("20060102T150405")))
	if err := os.WriteFile(tracePath, endResult.GoTrace, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "error: write trace output: %v\n", err)
		return 1
	}
	fmt.Printf("Trace saved: %s (%d bytes)\n", tracePath, len(endResult.GoTrace))
	if strings.TrimSpace(endResult.Status.TracePath) != "" {
		fmt.Printf("Daemon trace path: %s\n", endResult.Status.TracePath)
	}

	jsonPath := strings.TrimSuffix(tracePath, filepath.Ext(tracePath)) + ".perfetto.json"
	taskCount, err := exportJSONTraceFromGoToolTrace(tracePath, jsonPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: export perfetto json: %v\n", err)
		fmt.Fprintf(os.Stderr, "raw trace retained at %s\n", tracePath)
		return 1
	}
	fmt.Printf("Perfetto JSON exported: %s\n", jsonPath)
	fmt.Printf("Merged task-focused slices from %d task(s)\n", taskCount)
	semanticFiltered, droppedRoots := dropShortHiddenSemanticRoots(endResult.SemanticTrace, omitHiddenRootBelowMS)
	if droppedRoots > 0 {
		fmt.Printf("Dropped %d short hidden semantic root task(s) (< %.3fms)\n", droppedRoots, omitHiddenRootBelowMS)
	}
	if err := stitchSemanticTraceIntoJSON(jsonPath, semanticFiltered); err != nil {
		fmt.Fprintf(os.Stderr, "error: stitch semantic trace: %v\n", err)
		return 1
	}
	fmt.Printf("Stitched semantic events: %d\n", len(semanticFiltered.Events))

	if err := servePerfettoUIUntilInterrupt(jsonPath, serveHTTPAddr); err != nil {
		fmt.Fprintf(os.Stderr, "error: trace server: %v\n", err)
		return 1
	}
	return 0
}

func waitForEndCommand() error {
	fmt.Println("Tracing in progress. Type '/end' and press Enter to stop.")
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for {
			fmt.Print("> ")
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					errCh <- err
					return
				}
				errCh <- fmt.Errorf("stdin closed before '/end'")
				return
			}
			lineCh <- strings.TrimSpace(scanner.Text())
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for {
		select {
		case sig := <-sigCh:
			return fmt.Errorf("received signal %s before '/end'", sig)
		case err := <-errCh:
			return err
		case line := <-lineCh:
			if line == "/end" {
				return nil
			}
			fmt.Println("Type '/end' to stop tracing.")
		}
	}
}

func stitchSemanticTraceIntoJSON(jsonPath string, dump semtrace.Dump) error {
	if len(dump.Events) == 0 {
		return nil
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return err
	}
	merged, err := appendSemanticEventsToTraceJSON(data, dump)
	if err != nil {
		return err
	}
	return os.WriteFile(jsonPath, merged, 0o600)
}

func dropShortHiddenSemanticRoots(dump semtrace.Dump, thresholdMS float64) (semtrace.Dump, int) {
	if thresholdMS <= 0 || len(dump.Events) == 0 {
		return dump, 0
	}
	thresholdNS := int64(thresholdMS * float64(time.Millisecond))
	taskStarts := map[uint64]semtrace.Event{}
	taskEnds := map[uint64]semtrace.Event{}
	parentByTask := map[uint64]uint64{}
	regionTask := map[uint64]uint64{}
	for _, ev := range dump.Events {
		switch ev.Kind {
		case "task_start":
			taskStarts[ev.TaskID] = ev
			parentByTask[ev.TaskID] = ev.ParentTaskID
		case "task_end":
			taskEnds[ev.TaskID] = ev
		case "region_start":
			if ev.TaskID > 0 {
				regionTask[ev.RegionID] = ev.TaskID
			}
		}
	}

	dropRoots := map[uint64]struct{}{}
	for taskID, start := range taskStarts {
		if start.ParentTaskID != 0 {
			continue
		}
		if !hiddenFromLogDefault(start.Attrs) {
			continue
		}
		end, ok := taskEnds[taskID]
		if !ok {
			continue
		}
		if end.TsNS-start.TsNS < thresholdNS {
			dropRoots[taskID] = struct{}{}
		}
	}
	if len(dropRoots) == 0 {
		return dump, 0
	}

	dropTask := map[uint64]struct{}{}
	for taskID := range taskStarts {
		root := findTaskRoot(taskID, parentByTask)
		if _, ok := dropRoots[root]; ok {
			dropTask[taskID] = struct{}{}
		}
	}

	filtered := make([]semtrace.Event, 0, len(dump.Events))
	for _, ev := range dump.Events {
		switch ev.Kind {
		case "region_end":
			taskID := regionTask[ev.RegionID]
			if _, ok := dropTask[taskID]; ok {
				continue
			}
		default:
			if _, ok := dropTask[ev.TaskID]; ok {
				continue
			}
		}
		filtered = append(filtered, ev)
	}
	dump.Events = filtered
	return dump, len(dropRoots)
}

func hiddenFromLogDefault(attrs map[string]any) bool {
	if len(attrs) == 0 {
		return false
	}
	for _, key := range []string{"hide_from_log_by_default", "hide_by_default"} {
		v, ok := attrs[key]
		if !ok {
			continue
		}
		b, ok := v.(bool)
		if ok && b {
			return true
		}
	}
	return false
}

func findTaskRoot(taskID uint64, parentByTask map[uint64]uint64) uint64 {
	if taskID == 0 {
		return 0
	}
	seen := map[uint64]struct{}{}
	cur := taskID
	for cur != 0 {
		if _, loop := seen[cur]; loop {
			break
		}
		seen[cur] = struct{}{}
		parent, ok := parentByTask[cur]
		if !ok || parent == 0 {
			return cur
		}
		cur = parent
	}
	return taskID
}

func appendSemanticEventsToTraceJSON(baseJSON []byte, dump semtrace.Dump) ([]byte, error) {
	var base map[string]any
	if err := json.Unmarshal(baseJSON, &base); err != nil {
		return nil, fmt.Errorf("decode base jsontrace: %w", err)
	}
	eventsAny, ok := base["traceEvents"].([]any)
	if !ok {
		return nil, fmt.Errorf("base jsontrace missing traceEvents")
	}

	const semanticPID = 2000
	eventsAny = append(eventsAny, map[string]any{
		"name": "process_name",
		"ph":   "M",
		"pid":  semanticPID,
		"tid":  0,
		"ts":   0,
		"args": map[string]any{"name": "zoa-semantic"},
	})

	taskStart := map[uint64]semtrace.Event{}
	regionStart := map[uint64]semtrace.Event{}
	for _, ev := range dump.Events {
		switch ev.Kind {
		case "task_start":
			taskStart[ev.TaskID] = ev
		case "task_end":
			start, ok := taskStart[ev.TaskID]
			if !ok {
				continue
			}
			tid := int(start.TaskID)
			eventsAny = append(eventsAny, map[string]any{
				"name": start.Name,
				"ph":   "X",
				"pid":  semanticPID,
				"tid":  tid,
				"ts":   float64(start.TsNS) / 1000.0,
				"dur":  float64(ev.TsNS-start.TsNS) / 1000.0,
				"args": semanticArgsWithOverride(start, ev.Attrs, map[string]any{
					"task_id":        start.TaskID,
					"parent_task_id": start.ParentTaskID,
					"kind":           "task",
				}),
			})
			eventsAny = append(eventsAny, map[string]any{
				"name": "thread_name",
				"ph":   "M",
				"pid":  semanticPID,
				"tid":  tid,
				"ts":   0,
				"args": map[string]any{"name": fmt.Sprintf("task.%d %s", start.TaskID, start.Name)},
			})
		case "region_start":
			regionStart[ev.RegionID] = ev
		case "region_end":
			start, ok := regionStart[ev.RegionID]
			if !ok {
				continue
			}
			tid := int(start.TaskID)
			if tid == 0 {
				tid = 1
			}
			eventsAny = append(eventsAny, map[string]any{
				"name": start.Name,
				"ph":   "X",
				"pid":  semanticPID,
				"tid":  tid,
				"ts":   float64(start.TsNS) / 1000.0,
				"dur":  float64(ev.TsNS-start.TsNS) / 1000.0,
				"args": semanticArgsWithOverride(start, ev.Attrs, map[string]any{
					"task_id":          start.TaskID,
					"region_id":        start.RegionID,
					"parent_region_id": start.ParentRegionID,
					"kind":             "region",
				}),
			})
		case "log":
			tid := int(ev.TaskID)
			if tid == 0 {
				tid = 1
			}
			eventsAny = append(eventsAny, map[string]any{
				"name": fmt.Sprintf("[%s] %s", ev.Category, ev.Message),
				"ph":   "I",
				"s":    "t",
				"pid":  semanticPID,
				"tid":  tid,
				"ts":   float64(ev.TsNS) / 1000.0,
				"args": semanticArgs(ev, map[string]any{
					"task_id":   ev.TaskID,
					"region_id": ev.RegionID,
					"kind":      "log",
				}),
			})
		}
	}
	base["traceEvents"] = eventsAny
	out, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("encode stitched jsontrace: %w", err)
	}
	return out, nil
}

func semanticArgs(ev semtrace.Event, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range ev.Attrs {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	if ev.Category != "" {
		out["category"] = ev.Category
	}
	if ev.Message != "" {
		out["message"] = ev.Message
	}
	return out
}

func semanticArgsWithOverride(base semtrace.Event, override map[string]any, extra map[string]any) map[string]any {
	out := semanticArgs(base, extra)
	for k, v := range override {
		out[k] = v
	}
	return out
}

func normalizeTraceControlURL(addrOrURL string) (string, error) {
	raw := strings.TrimSpace(addrOrURL)
	if raw == "" {
		return "", fmt.Errorf("value cannot be empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("missing host")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func exportJSONTraceFromGoToolTrace(tracePath string, outPath string) (int, error) {
	addr, err := allocateLoopbackAddr()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command("go", "tool", "trace", "-http="+addr, tracePath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start go tool trace: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	fullJSON, exportErr := fetchJSONTrace(waitCh, addr, "/jsontrace", 30*time.Second)
	taskCount := 0
	if exportErr == nil {
		taskIDs, discoverErr := discoverAllTaskIDs(waitCh, addr, 10*time.Second)
		if discoverErr != nil {
			exportErr = discoverErr
		} else {
			taskJSONs := make([][]byte, 0, len(taskIDs))
			for _, taskID := range taskIDs {
				path := "/jsontrace?taskid=" + strconv.FormatUint(taskID, 10)
				taskJSON, taskErr := fetchJSONTrace(waitCh, addr, path, 30*time.Second)
				if taskErr != nil {
					exportErr = fmt.Errorf("fetch task %d trace: %w", taskID, taskErr)
					break
				}
				taskJSONs = append(taskJSONs, taskJSON)
			}
			if exportErr == nil {
				merged, mergeErr := mergeJSONTraces(fullJSON, taskJSONs)
				if mergeErr != nil {
					exportErr = mergeErr
				} else if err := os.WriteFile(outPath, merged, 0o600); err != nil {
					exportErr = err
				} else {
					taskCount = len(taskIDs)
				}
			}
		}
	}
	stopChildProcess(cmd, waitCh)
	if exportErr != nil {
		return 0, exportErr
	}
	return taskCount, nil
}

func fetchJSONTrace(waitCh <-chan error, serveHTTPAddr string, jsonPath string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	url := fmt.Sprintf("http://%s%s", strings.TrimSpace(serveHTTPAddr), jsonPath)
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		select {
		case err := <-waitCh:
			if err != nil {
				return nil, fmt.Errorf("go tool trace exited before /jsontrace was ready: %w", err)
			}
			return nil, fmt.Errorf("go tool trace exited before /jsontrace was ready")
		default:
		}

		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			_ = resp.Body.Close()
			time.Sleep(250 * time.Millisecond)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if len(body) <= 0 {
			lastErr = fmt.Errorf("empty /jsontrace response")
			time.Sleep(250 * time.Millisecond)
			continue
		}
		return body, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for %s", url)
	}
	return nil, fmt.Errorf("fetch json trace: %w", lastErr)
}

func discoverAllTaskIDs(waitCh <-chan error, serveHTTPAddr string, timeout time.Duration) ([]uint64, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	base := fmt.Sprintf("http://%s", strings.TrimSpace(serveHTTPAddr))
	usertasksURL := base + "/usertasks"
	deadline := time.Now().Add(timeout)
	var html string
	for time.Now().Before(deadline) {
		select {
		case err := <-waitCh:
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("go tool trace exited before /usertasks was ready")
		default:
		}
		resp, err := client.Get(usertasksURL)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr == nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
			html = string(body)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if strings.TrimSpace(html) == "" {
		return nil, fmt.Errorf("could not load /usertasks")
	}

	typeRE := regexp.MustCompile(`/usertask\?type=([^"&]+)`)
	typeMatches := typeRE.FindAllStringSubmatch(html, -1)
	if len(typeMatches) == 0 {
		return []uint64{}, nil
	}
	typeSet := map[string]struct{}{}
	for _, m := range typeMatches {
		if len(m) != 2 {
			continue
		}
		t, err := url.QueryUnescape(m[1])
		if err != nil || strings.TrimSpace(t) == "" {
			continue
		}
		typeSet[t] = struct{}{}
	}
	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, t)
	}
	sort.Strings(types)

	taskIDSet := map[uint64]struct{}{}
	taskRE := regexp.MustCompile(`/trace\?taskid=(\d+)`)
	for _, taskType := range types {
		taskPageURL := base + "/usertask?type=" + url.QueryEscape(taskType)
		resp, err := client.Get(taskPageURL)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		matches := taskRE.FindAllStringSubmatch(string(body), -1)
		for _, m := range matches {
			if len(m) != 2 {
				continue
			}
			id, err := strconv.ParseUint(m[1], 10, 64)
			if err == nil && id > 0 {
				taskIDSet[id] = struct{}{}
			}
		}
	}
	taskIDs := make([]uint64, 0, len(taskIDSet))
	for id := range taskIDSet {
		taskIDs = append(taskIDs, id)
	}
	sort.Slice(taskIDs, func(i, j int) bool {
		return taskIDs[i] < taskIDs[j]
	})
	return taskIDs, nil
}

func mergeJSONTraces(fullJSON []byte, taskJSONs [][]byte) ([]byte, error) {
	var base map[string]any
	if err := json.Unmarshal(fullJSON, &base); err != nil {
		return nil, fmt.Errorf("decode full jsontrace: %w", err)
	}
	baseEventsAny, ok := base["traceEvents"].([]any)
	if !ok {
		return nil, fmt.Errorf("full jsontrace missing traceEvents")
	}
	seen := make(map[string]struct{}, len(baseEventsAny))
	for _, ev := range baseEventsAny {
		keyBytes, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		seen[string(keyBytes)] = struct{}{}
	}

	for _, taskJSON := range taskJSONs {
		var one map[string]any
		if err := json.Unmarshal(taskJSON, &one); err != nil {
			return nil, fmt.Errorf("decode task jsontrace: %w", err)
		}
		eventsAny, ok := one["traceEvents"].([]any)
		if !ok {
			continue
		}
		for _, ev := range eventsAny {
			keyBytes, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			key := string(keyBytes)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			baseEventsAny = append(baseEventsAny, ev)
		}
	}
	base["traceEvents"] = baseEventsAny
	merged, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("encode merged jsontrace: %w", err)
	}
	return merged, nil
}

func servePerfettoUIUntilInterrupt(jsonPath string, serveHTTPAddr string) error {
	host, port, err := splitHostPortOrDefault(serveHTTPAddr)
	if err != nil {
		return err
	}
	traceProcessorPath, err := resolveTraceProcessorBinary()
	if err != nil {
		return err
	}

	cmd := exec.Command(traceProcessorPath, "-D", "--http-port", port, "--http-ip-address", host, jsonPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start trace_processor: %w", err)
	}

	fmt.Printf("Trace Processor RPC running at http://%s:%s\n", host, port)
	fmt.Println("Open https://ui.perfetto.dev and click YES on native acceleration.")
	fmt.Println("Press Ctrl-C to stop.")

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-waitCh:
		if err != nil {
			return fmt.Errorf("trace_processor exited: %w", err)
		}
		return nil
	case <-sigCh:
		stopChildProcess(cmd, waitCh)
		return nil
	}
}

func splitHostPortOrDefault(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", fmt.Errorf("serve http address cannot be empty")
	}
	if strings.Contains(addr, "://") {
		return "", "", fmt.Errorf("serve http address must be host:port")
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1", strings.TrimPrefix(addr, ":"), nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("invalid serve http address %q: %w", addr, err)
	}
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	if strings.TrimSpace(port) == "" {
		return "", "", fmt.Errorf("invalid serve http address %q: missing port", addr)
	}
	return host, port, nil
}

func resolveTraceProcessorBinary() (string, error) {
	if path, err := exec.LookPath("trace_processor"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		fallback := filepath.Join(home, ".local", "bin", "trace_processor")
		if _, statErr := os.Stat(fallback); statErr == nil {
			return fallback, nil
		}
	}
	if path, err := exec.LookPath("trace_processor_shell"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("trace_processor not found (checked PATH and ~/.local/bin/trace_processor)")
}

func allocateLoopbackAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate local port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

func stopChildProcess(cmd *exec.Cmd, waitCh <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-waitCh:
		return
	case <-time.After(2 * time.Second):
	}
	_ = cmd.Process.Kill()
	select {
	case <-waitCh:
	case <-time.After(1 * time.Second):
	}
}
