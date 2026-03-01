package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	baselineagent "zoa/baselineagent"
)

type Store struct {
	snapshotPath string
	mu           sync.Mutex
}

func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("store dir cannot be empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve store dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	return &Store{
		snapshotPath: filepath.Join(abs, "snapshot.json"),
	}, nil
}

func (s *Store) LoadSnapshot(sessionID string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			now := time.Now().UTC()
			return Snapshot{
				SessionID:    sessionID,
				UpdatedAt:    now,
				Queue:        []InboundMessage{},
				Outbox:       []OutboundMessage{},
				Conversation: []baselineagent.ConversationMessage{},
			}, nil
		}
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if snap.SessionID == "" {
		snap.SessionID = sessionID
	}
	if snap.Queue == nil {
		snap.Queue = []InboundMessage{}
	}
	if snap.Outbox == nil {
		snap.Outbox = []OutboundMessage{}
	}
	if snap.Conversation == nil {
		snap.Conversation = []baselineagent.ConversationMessage{}
	}
	return snap, nil
}

func (s *Store) SaveSnapshot(snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveSnapshotLocked(snap)
}

func (s *Store) saveSnapshotLocked(snap Snapshot) error {
	snap.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	tmp := s.snapshotPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp snapshot: %w", err)
	}
	if err := os.Rename(tmp, s.snapshotPath); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	return nil
}
