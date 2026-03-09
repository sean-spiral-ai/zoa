package db

const schemaSQL = `
CREATE TABLE IF NOT EXISTS conversation_node (
	hash         TEXT PRIMARY KEY,
	parent_hash  TEXT,
	role         TEXT NOT NULL,
	message_json TEXT NOT NULL,
	created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_node_parent
	ON conversation_node(parent_hash);

CREATE TABLE IF NOT EXISTS conversation_ref (
	name        TEXT PRIMARY KEY,
	hash        TEXT NOT NULL,
	leased_by   TEXT NOT NULL DEFAULT '',
	lease_until TEXT NOT NULL DEFAULT '',
	updated_at  TEXT NOT NULL
);
`
