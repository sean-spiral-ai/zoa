package conversation

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"

	convdb "zoa/conversation/db"
)

// StartServer starts an HTTP server serving the conversation tree API and WebUI.
// Returns the server and listener for cleanup.
func StartServer(addr string, db *convdb.DB) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/nodes", func(w http.ResponseWriter, r *http.Request) {
		sinceStr := r.URL.Query().Get("since_id")
		var sinceID int64
		if sinceStr != "" {
			v, err := strconv.ParseInt(sinceStr, 10, 64)
			if err != nil {
				http.Error(w, "invalid since_id", http.StatusBadRequest)
				return
			}
			sinceID = v
		}
		nodes, err := db.TraceNodesSince(sinceID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodes)
	})
	mux.HandleFunc("/api/tree", func(w http.ResponseWriter, r *http.Request) {
		nodes, err := db.TraceAllNodes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodes)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, webUI)
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen conversation server: %w", err)
	}

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return srv, ln, nil
}

var webUI = "<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n<title>Conversation Tree</title>\n<style>\n" +
	"* { box-sizing: border-box; margin: 0; padding: 0; }\n" +
	"body { font-family: -apple-system, BlinkMacSystemFont, \"Segoe UI\", Roboto, monospace; background: #0d1117; color: #c9d1d9; padding: 16px; }\n" +
	"h1 { margin-bottom: 12px; font-size: 1.4em; color: #58a6ff; }\n" +
	".stats { font-size: 0.85em; color: #8b949e; margin-bottom: 16px; }\n" +
	".tree { margin-left: 0; }\n" +
	".baseline { cursor: pointer; padding: 4px 8px; border-radius: 4px; display: flex; align-items: center; gap: 8px; }\n" +
	".baseline:hover { background: #161b22; }\n" +
	".role { font-size: 0.75em; padding: 2px 6px; border-radius: 3px; font-weight: 600; text-transform: uppercase; flex-shrink: 0; }\n" +
	".role-hash { font-size: 0.72em; color: #6e7681; letter-spacing: 0.04em; flex-shrink: 0; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }\n" +
	".role-system { background: #1f2937; color: #9ca3af; }\n" +
	".role-user { background: #0c2d48; color: #58a6ff; }\n" +
	".role-assistant { background: #1a2e1a; color: #3fb950; }\n" +
	".role-tool { background: #2d1f00; color: #d29922; }\n" +
	".role-root { background: #1c1c1c; color: #666; }\n" +
	".preview { font-size: 0.85em; color: #8b949e; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0; }\n" +
	".time { font-size: 0.7em; color: #484f58; flex-shrink: 0; }\n" +
	".min-icon { color: #484f58; flex-shrink: 0; font-size: 0.75em; width: 12px; text-align: center; cursor: pointer; }\n" +
	".expand-icon { color: #484f58; flex-shrink: 0; font-size: 0.7em; width: 12px; text-align: center; cursor: pointer; }\n" +
	".expanded { display: none; margin: 2px 0 4px 32px; background: #161b22; border-radius: 6px; padding: 12px; font-size: 0.82em; overflow-x: auto; }\n" +
	".expanded.open { display: block; }\n" +
	".expanded pre { white-space: pre-wrap; word-break: break-word; color: #c9d1d9; margin: 0; }\n" +
	".expanded .section { margin-bottom: 10px; }\n" +
	".expanded .section:last-child { margin-bottom: 0; }\n" +
	".expanded .section-label { color: #58a6ff; font-weight: 600; margin-bottom: 4px; display: block; font-size: 0.9em; }\n" +
	".expanded .tool-call { background: #1c2333; border-radius: 4px; padding: 8px; margin-bottom: 6px; }\n" +
	".expanded .tool-call:last-child { margin-bottom: 0; }\n" +
	".expanded .tool-name { color: #d2a8ff; font-weight: 600; }\n" +
	".expanded .tool-id { color: #484f58; font-size: 0.85em; margin-left: 8px; }\n" +
	".expanded .tool-args { margin-top: 4px; }\n" +
	".expanded .tool-result { background: #1c2333; border-radius: 4px; padding: 8px; margin-bottom: 6px; }\n" +
	".expanded .tool-result:last-child { margin-bottom: 0; }\n" +
	".expanded .tool-result-header { display: flex; gap: 8px; align-items: center; }\n" +
	".expanded .tool-result-status { font-size: 0.8em; padding: 1px 5px; border-radius: 3px; }\n" +
	".expanded .tool-result-ok { background: #1a2e1a; color: #3fb950; }\n" +
	".expanded .tool-result-err { background: #3d1f1f; color: #f85149; }\n" +
	".expanded .tool-result-output { margin-top: 4px; max-height: 300px; overflow-y: auto; }\n" +
	".expanded .text-content { color: #c9d1d9; line-height: 1.5; }\n" +
	".iso-toggle { cursor: pointer; color: #484f58; font-size: 0.75em; margin-top: 8px; display: inline-block; border-bottom: 1px dotted #484f58; }\n" +
	".iso-toggle:hover { color: #8b949e; }\n" +
	".iso { display: none; margin-top: 8px; background: #0d1117; border: 1px solid #30363d; border-radius: 4px; padding: 8px; font-size: 0.85em; }\n" +
	".iso.open { display: block; }\n" +
	".iso .iso-label { color: #484f58; font-weight: 600; font-size: 0.85em; margin-top: 6px; display: block; }\n" +
	".iso .iso-label:first-child { margin-top: 0; }\n" +
	".iso pre { white-space: pre-wrap; word-break: break-all; color: #8b949e; }\n" +
	".fork-group { margin-left: 20px; border-left: 2px solid #30363d; padding-left: 8px; margin-top: 4px; margin-bottom: 4px; }\n" +
	".fork-label { font-size: 0.7em; color: #484f58; margin-bottom: 2px; padding-left: 4px; }\n" +
	"</style>\n</head>\n<body>\n" +
	"<h1>Conversation Tree</h1>\n" +
	"<div class=\"stats\" id=\"stats\">Loading...</div>\n" +
	"<div class=\"tree\" id=\"tree\"></div>\n" +
	"<script>\n" +
	webUIScript +
	"</script>\n</body>\n</html>\n"
