package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zoa/internal/agentloop/llm"
	gatewaylmf "zoa/hub/util/gateway"

	_ "modernc.org/sqlite"
)

func runInspect(args []string) int {
	inspectFlags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	inspectFlags.SetOutput(os.Stderr)

	var (
		sessionDir string
		sqlitePath string
		limit      int
		session    string
	)
	inspectFlags.StringVar(&sessionDir, "session-dir", ".gateway/sessions/default", "Session directory containing runtime.db, state.db, and conversation.db")
	inspectFlags.StringVar(&sqlitePath, "sqlite-path", "", "Explicit SQLite path (overrides the default DB selected from --session-dir)")
	inspectFlags.IntVar(&limit, "limit", 50, "Max rows to print for SQL query results")
	inspectFlags.StringVar(&session, "session", "default", "Gateway session id (used by inspect conversation)")

	if err := inspectFlags.Parse(args); err != nil {
		return 2
	}
	if limit <= 0 {
		limit = 20
	}
	rest := inspectFlags.Args()
	defaultPath := filepath.Join(sessionDir, "state.db")
	if len(rest) > 0 && rest[0] == "conversation" {
		defaultPath = filepath.Join(sessionDir, "conversation.db")
	}
	if strings.TrimSpace(sqlitePath) == "" {
		sqlitePath = defaultPath
	}
	absPath, err := filepath.Abs(sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error resolving sqlite path: %v\n", err)
		return 1
	}

	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening sqlite: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "error pinging sqlite: %v\n", err)
		return 1
	}

	if len(rest) == 0 {
		return runInspectSummary(absPath, db)
	}

	switch rest[0] {
	case "sql":
		query := strings.TrimSpace(strings.Join(rest[1:], " "))
		if query == "" {
			fmt.Fprintln(os.Stderr, "error: missing SQL query")
			fmt.Fprintln(os.Stderr, "usage: zoa inspect [flags] sql \"SELECT ...\"")
			return 2
		}
		return runInspectSQL(db, query, limit)
	case "conversation":
		return runInspectConversation(db, session)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown inspect subcommand %q\n", rest[0])
		fmt.Fprintln(os.Stderr, "usage:")
		fmt.Fprintln(os.Stderr, "  zoa inspect [flags]")
		fmt.Fprintln(os.Stderr, "  zoa inspect [flags] sql \"<sql>\"")
		fmt.Fprintln(os.Stderr, "  zoa inspect [flags] conversation")
		return 2
	}
}

func runInspectSummary(sqlitePath string, db *sql.DB) int {
	fmt.Printf("SQLite: %s\n", sqlitePath)
	if info, err := os.Stat(sqlitePath); err == nil {
		fmt.Printf("File size: %d bytes\n", info.Size())
	}

	pageSize, err := sqlitePragmaInt(db, "page_size")
	if err == nil {
		pageCount, countErr := sqlitePragmaInt(db, "page_count")
		freelistCount, freeErr := sqlitePragmaInt(db, "freelist_count")
		if countErr == nil {
			fmt.Printf("Pages: size=%d count=%d total=%d bytes\n", pageSize, pageCount, pageSize*pageCount)
		}
		if freeErr == nil {
			fmt.Printf("Freelist pages: %d\n", freelistCount)
		}
	}

	tables, err := sqliteTables(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading tables: %v\n", err)
		return 1
	}
	fmt.Printf("Tables: %d\n", len(tables))
	if len(tables) == 0 {
		return 0
	}
	fmt.Println("")
	for _, name := range tables {
		count, err := tableCount(db, name)
		if err != nil {
			fmt.Printf("- %s (count error: %v)\n", name, err)
			continue
		}
		fmt.Printf("- %s rows=%d\n", name, count)
	}
	return 0
}

func runInspectSQL(db *sql.DB, query string, limit int) int {
	if limit <= 0 {
		limit = 50
	}
	fmt.Printf("SQL> %s\n", query)
	rows, queryErr := db.Query(query)
	if queryErr != nil {
		res, execErr := db.Exec(query)
		if execErr != nil {
			fmt.Fprintf(os.Stderr, "sql error: %v\n", queryErr)
			return 1
		}
		rowsAffected, _ := res.RowsAffected()
		lastInsertID, lastErr := res.LastInsertId()
		if lastErr == nil {
			fmt.Printf("OK rows_affected=%d last_insert_id=%d\n", rowsAffected, lastInsertID)
			return 0
		}
		fmt.Printf("OK rows_affected=%d\n", rowsAffected)
		return 0
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sql columns error: %v\n", err)
		return 1
	}
	fmt.Println(strings.Join(cols, " | "))

	values := make([]any, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	rowCount := 0
	printed := 0
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			fmt.Fprintf(os.Stderr, "sql scan error: %v\n", err)
			return 1
		}
		rowCount++
		if printed >= limit {
			continue
		}
		cells := make([]string, len(values))
		for i := range values {
			cells[i] = formatSQLCell(values[i])
		}
		fmt.Println(strings.Join(cells, " | "))
		printed++
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "sql rows error: %v\n", err)
		return 1
	}
	if rowCount > printed {
		fmt.Printf("(%d rows total; showing first %d)\n", rowCount, printed)
	} else {
		fmt.Printf("(%d rows)\n", rowCount)
	}
	return 0
}

func runInspectConversation(db *sql.DB, session string) int {
	events, err := gatewaylmf.DebugGetConversation(context.Background(), db, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading conversation: %v\n", err)
		return 1
	}

	session = strings.TrimSpace(session)
	if session == "" {
		session = "default"
	}
	fmt.Printf("Conversation session=%s\n", session)
	fmt.Printf("Events: %d\n", len(events))
	if len(events) == 0 {
		return 0
	}
	fmt.Println("")

	for _, ev := range events {
		role := strings.TrimSpace(string(ev.Message.Role))
		if role == "" {
			role = "unknown"
		}
		fmt.Printf("MESSAGE [%d] %s %s\n", ev.ID, ev.CreatedAt, role)
		fmt.Println(renderInspectConversationMessage(ev.Message))
		fmt.Println("")
	}
	return 0
}

func renderInspectConversationMessage(msg llm.Message) string {
	sections := []string{}
	primaryText := strings.TrimSpace(msg.Text)
	if primaryText != "" {
		sections = append(sections, primaryText)
	}

	partTexts := []string{}
	for _, p := range msg.Parts {
		if text := strings.TrimSpace(p.Text); text != "" {
			if primaryText != "" && text == primaryText {
				continue
			}
			partTexts = append(partTexts, text)
		}
	}
	if len(partTexts) > 0 {
		sections = append(sections, strings.Join(partTexts, "\n\n"))
	}

	toolCalls := []string{}
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, formatInspectToolCall(tc))
		}
	} else {
		for _, p := range msg.Parts {
			if p.ToolCall != nil {
				toolCalls = append(toolCalls, formatInspectToolCall(*p.ToolCall))
			}
		}
	}
	if len(toolCalls) > 0 {
		sections = append(sections, strings.Join(toolCalls, "\n"))
	}

	toolResults := []string{}
	for _, tr := range msg.ToolResults {
		status := "ok"
		if tr.IsError {
			status = "error"
		}
		output := strings.TrimSpace(tr.Output)
		if output == "" {
			output = "(no output)"
		}
		toolResults = append(toolResults, fmt.Sprintf("tool_result call_id=%s name=%s status=%s\n%s", strings.TrimSpace(tr.CallID), strings.TrimSpace(tr.Name), status, output))
	}
	if len(toolResults) > 0 {
		sections = append(sections, strings.Join(toolResults, "\n\n"))
	}

	if len(sections) == 0 {
		return "(empty)"
	}
	return strings.Join(sections, "\n\n")
}

func formatInspectToolCall(call llm.ToolCall) string {
	argsJSON := "{}"
	if len(call.Args) > 0 {
		if b, err := json.Marshal(call.Args); err == nil {
			argsJSON = string(b)
		}
	}
	return fmt.Sprintf("tool_call id=%s name=%s args=%s", strings.TrimSpace(call.ID), strings.TrimSpace(call.Name), argsJSON)
}

func sqlitePragmaInt(db *sql.DB, name string) (int64, error) {
	row := db.QueryRow("PRAGMA " + name)
	var out int64
	if err := row.Scan(&out); err != nil {
		return 0, err
	}
	return out, nil
}

func sqliteTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func tableCount(db *sql.DB, tableName string) (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteSQLiteIdent(tableName))
	row := db.QueryRow(query)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func formatSQLCell(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return formatSQLCell(string(t))
	case string:
		s := strings.ReplaceAll(t, "\n", "\\n")
		s = strings.TrimSpace(s)
		if s == "" {
			return `""`
		}
		return s
	default:
		return fmt.Sprintf("%v", t)
	}
}
