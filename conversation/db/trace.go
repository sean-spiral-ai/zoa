package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

func (db *DB) TraceNodesSince(lastID int64) ([]TraceNode, error) {
	rows, err := db.sql.Query(
		`SELECT rowid, hash, parent_hash, role, message_json, created_at
		 FROM conversation_node
		 WHERE rowid > ?
		 ORDER BY rowid`,
		lastID,
	)
	if err != nil {
		return nil, fmt.Errorf("trace nodes since: %w", err)
	}
	defer rows.Close()
	return scanTraceNodes(rows)
}

func (db *DB) TraceAllNodes() ([]TraceNode, error) {
	rows, err := db.sql.Query(
		`SELECT rowid, hash, parent_hash, role, message_json, created_at
		 FROM conversation_node
		 ORDER BY rowid`,
	)
	if err != nil {
		return nil, fmt.Errorf("trace all nodes: %w", err)
	}
	defer rows.Close()
	return scanTraceNodes(rows)
}

func scanTraceNodes(rows *sql.Rows) ([]TraceNode, error) {
	nodes := make([]TraceNode, 0)
	for rows.Next() {
		var node TraceNode
		if err := rows.Scan(&node.ID, &node.Hash, &node.ParentHash, &node.Role, &node.MessageJSON, &node.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan trace node: %w", err)
		}
		node.Summary = messageSummaryFromJSON(node.Role, node.MessageJSON)
		node.MetadataJSON = "{}"
		if node.Role == "root" {
			node.Summary = "ROOT"
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trace nodes: %w", err)
	}
	return nodes, nil
}

func messageSummaryFromJSON(role string, messageJSON string) string {
	var msg Message
	if err := json.Unmarshal([]byte(messageJSON), &msg); err != nil {
		return ""
	}
	if text := msg.Text; text != "" {
		if len(text) > 200 {
			return text[:200]
		}
		return text
	}
	if len(msg.ToolCalls) > 0 {
		name := msg.ToolCalls[0].Name
		if name != "" {
			return fmt.Sprintf("[tool_call: %s]", name)
		}
		return "[tool_calls]"
	}
	if len(msg.ToolResults) > 0 {
		name := msg.ToolResults[0].Name
		if name != "" {
			return fmt.Sprintf("[tool_result: %s]", name)
		}
		return "[tool_results]"
	}
	if role == "root" {
		return "ROOT"
	}
	return ""
}
