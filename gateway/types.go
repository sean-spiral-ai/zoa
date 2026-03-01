package gateway

import (
	"time"

	baselineagent "codexagentbase/baselineagent"
)

type InboundMessage struct {
	ID         int64     `json:"id"`
	Channel    string    `json:"channel"`
	Text       string    `json:"text"`
	ReceivedAt time.Time `json:"received_at"`
}

type OutboundMessage struct {
	ID        int64     `json:"id"`
	Channel   string    `json:"channel"`
	Text      string    `json:"text"`
	InReplyTo int64     `json:"in_reply_to,omitempty"`
	SentAt    time.Time `json:"sent_at"`
}

type ActiveRun struct {
	MessageID int64     `json:"message_id"`
	Channel   string    `json:"channel"`
	StartedAt time.Time `json:"started_at"`
}

type Snapshot struct {
	SessionID      string                              `json:"session_id"`
	UpdatedAt      time.Time                           `json:"updated_at"`
	NextInboundID  int64                               `json:"next_inbound_id"`
	NextOutboundID int64                               `json:"next_outbound_id"`
	Queue          []InboundMessage                    `json:"queue"`
	Active         *ActiveRun                          `json:"active,omitempty"`
	Outbox         []OutboundMessage                   `json:"outbox"`
	Conversation   []baselineagent.ConversationMessage `json:"conversation"`
}

type ReceiveResult struct {
	Accepted  bool   `json:"accepted"`
	MessageID int64  `json:"message_id,omitempty"`
	Decision  string `json:"decision"`
	QueueLen  int    `json:"queue_len"`
}
