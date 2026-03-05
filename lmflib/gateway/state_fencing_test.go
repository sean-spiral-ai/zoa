package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	lmfrt "zoa/lmfrt"
)

func TestCompleteInboundSuccessFencedByAttempt(t *testing.T) {
	st, tc := newGatewayTestState(t)
	defer func() { _ = tc.Close() }()

	now := time.Now().UTC()
	inboundID, err := st.insertInbound("default", "gatewaychannel://test", "hello", nil, now)
	if err != nil {
		t.Fatalf("insert inbound: %v", err)
	}
	row, err := st.claimDueInbound("default", now, time.Minute)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	if row == nil || row.ID != inboundID {
		t.Fatalf("expected claimed inbound %d, got %#v", inboundID, row)
	}

	outboxID, claimed, err := st.completeInboundSuccess("default", row.Channel, "ok", row.ID, row.Attempt+1, now.Add(time.Second))
	if err != nil {
		t.Fatalf("complete inbound with wrong attempt: %v", err)
	}
	if claimed || outboxID != 0 {
		t.Fatalf("expected no-op on wrong attempt, claimed=%v outbox_id=%d", claimed, outboxID)
	}

	status := querySingleString(t, tc, `SELECT status FROM gateway__inbound WHERE id = ?`, row.ID)
	if status != "processing" {
		t.Fatalf("expected status processing after stale complete, got %q", status)
	}
	if got := querySingleInt64(t, tc, `SELECT COUNT(*) AS c FROM gateway__outbox`); got != 0 {
		t.Fatalf("expected outbox count 0 after stale complete, got %d", got)
	}

	outboxID, claimed, err = st.completeInboundSuccess("default", row.Channel, "ok", row.ID, row.Attempt, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("complete inbound with current attempt: %v", err)
	}
	if !claimed || outboxID == 0 {
		t.Fatalf("expected claimed complete with outbox row, claimed=%v outbox_id=%d", claimed, outboxID)
	}

	status = querySingleString(t, tc, `SELECT status FROM gateway__inbound WHERE id = ?`, row.ID)
	if status != "done" {
		t.Fatalf("expected status done, got %q", status)
	}
	if got := querySingleInt64(t, tc, `SELECT COUNT(*) AS c FROM gateway__outbox`); got != 1 {
		t.Fatalf("expected outbox count 1, got %d", got)
	}
}

func TestMarkInboundRetryFencedByAttempt(t *testing.T) {
	st, tc := newGatewayTestState(t)
	defer func() { _ = tc.Close() }()

	now := time.Now().UTC()
	inboundID, err := st.insertInbound("default", "gatewaychannel://test", "hello", nil, now)
	if err != nil {
		t.Fatalf("insert inbound: %v", err)
	}
	row, err := st.claimDueInbound("default", now, time.Minute)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	if row == nil || row.ID != inboundID {
		t.Fatalf("expected claimed inbound %d, got %#v", inboundID, row)
	}

	claimed, err := st.markInboundRetry(row.ID, row.Attempt+1, "stale attempt", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("retry with wrong attempt: %v", err)
	}
	if claimed {
		t.Fatalf("expected stale retry to be ignored")
	}

	claimed, err = st.markInboundRetry(row.ID, row.Attempt, "current attempt", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("retry with current attempt: %v", err)
	}
	if !claimed {
		t.Fatalf("expected retry for current attempt to be applied")
	}

	status := querySingleString(t, tc, `SELECT status FROM gateway__inbound WHERE id = ?`, row.ID)
	if status != "pending" {
		t.Fatalf("expected status pending, got %q", status)
	}
}

func TestCompleteInboundFailureFencedByAttempt(t *testing.T) {
	st, tc := newGatewayTestState(t)
	defer func() { _ = tc.Close() }()

	now := time.Now().UTC()
	inboundID, err := st.insertInbound("default", "gatewaychannel://test", "hello", nil, now)
	if err != nil {
		t.Fatalf("insert inbound: %v", err)
	}
	row, err := st.claimDueInbound("default", now, time.Minute)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	if row == nil || row.ID != inboundID {
		t.Fatalf("expected claimed inbound %d, got %#v", inboundID, row)
	}

	outboxID, claimed, err := st.completeInboundFailure("default", row.Channel, "failed reply", row.ID, row.Attempt+1, now.Add(time.Second), "stale failure")
	if err != nil {
		t.Fatalf("fail with wrong attempt: %v", err)
	}
	if claimed || outboxID != 0 {
		t.Fatalf("expected no-op on stale failure, claimed=%v outbox_id=%d", claimed, outboxID)
	}

	outboxID, claimed, err = st.completeInboundFailure("default", row.Channel, "failed reply", row.ID, row.Attempt, now.Add(2*time.Second), "current failure")
	if err != nil {
		t.Fatalf("fail with current attempt: %v", err)
	}
	if !claimed || outboxID == 0 {
		t.Fatalf("expected failure to be applied with outbox row, claimed=%v outbox_id=%d", claimed, outboxID)
	}

	status := querySingleString(t, tc, `SELECT status FROM gateway__inbound WHERE id = ?`, row.ID)
	if status != "failed" {
		t.Fatalf("expected status failed, got %q", status)
	}
	if got := querySingleInt64(t, tc, `SELECT COUNT(*) AS c FROM gateway__outbox`); got != 1 {
		t.Fatalf("expected outbox count 1, got %d", got)
	}
}

func TestInboundLeaseHeartbeatIntervalClamps(t *testing.T) {
	if got := inboundLeaseHeartbeatInterval(2 * time.Second); got != 5*time.Second {
		t.Fatalf("expected min heartbeat 5s, got %s", got)
	}
	if got := inboundLeaseHeartbeatInterval(2 * time.Minute); got != 30*time.Second {
		t.Fatalf("expected max heartbeat 30s, got %s", got)
	}
	if got := inboundLeaseHeartbeatInterval(18 * time.Second); got != 6*time.Second {
		t.Fatalf("expected lease/3 heartbeat, got %s", got)
	}
	if got := inboundLeaseHeartbeatInterval(0); got != 0 {
		t.Fatalf("expected disabled heartbeat when lease is 0, got %s", got)
	}
}

func TestClaimDueInboundStrictFIFOBlocksBehindProcessingHead(t *testing.T) {
	st, tc := newGatewayTestState(t)
	defer func() { _ = tc.Close() }()

	now := time.Now().UTC()
	firstID, err := st.insertInbound("default", "gatewaychannel://test", "first", nil, now)
	if err != nil {
		t.Fatalf("insert first inbound: %v", err)
	}
	secondID, err := st.insertInbound("default", "gatewaychannel://test", "second", nil, now.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("insert second inbound: %v", err)
	}

	first, err := st.claimDueInbound("default", now, time.Minute)
	if err != nil {
		t.Fatalf("claim first inbound: %v", err)
	}
	if first == nil || first.ID != firstID {
		t.Fatalf("expected first inbound id %d, got %#v", firstID, first)
	}

	blocked, err := st.claimDueInbound("default", now.Add(10*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("claim while head processing: %v", err)
	}
	if blocked != nil {
		t.Fatalf("expected no claim while head processing; got id=%d (second id=%d)", blocked.ID, secondID)
	}
}

func TestClaimDueInboundStrictFIFOBlocksBehindPendingBackoffHead(t *testing.T) {
	st, tc := newGatewayTestState(t)
	defer func() { _ = tc.Close() }()

	now := time.Now().UTC()
	firstID, err := st.insertInbound("default", "gatewaychannel://test", "first", nil, now)
	if err != nil {
		t.Fatalf("insert first inbound: %v", err)
	}
	secondID, err := st.insertInbound("default", "gatewaychannel://test", "second", nil, now.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("insert second inbound: %v", err)
	}

	first, err := st.claimDueInbound("default", now, time.Minute)
	if err != nil {
		t.Fatalf("claim first inbound: %v", err)
	}
	if first == nil || first.ID != firstID {
		t.Fatalf("expected first inbound id %d, got %#v", firstID, first)
	}

	retryAt := now.Add(5 * time.Minute)
	claimed, err := st.markInboundRetry(first.ID, first.Attempt, "backoff", retryAt)
	if err != nil {
		t.Fatalf("mark retry: %v", err)
	}
	if !claimed {
		t.Fatalf("expected markInboundRetry to claim current attempt")
	}

	blocked, err := st.claimDueInbound("default", now.Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("claim while head pending backoff: %v", err)
	}
	if blocked != nil {
		t.Fatalf("expected no claim while head pending backoff; got id=%d (second id=%d)", blocked.ID, secondID)
	}

	reclaimed, err := st.claimDueInbound("default", retryAt.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatalf("reclaim head after backoff: %v", err)
	}
	if reclaimed == nil || reclaimed.ID != firstID {
		t.Fatalf("expected reclaimed first inbound id %d, got %#v (second id=%d)", firstID, reclaimed, secondID)
	}
}

func TestClaimDueInboundStrictFIFOAdvancesAfterHeadDone(t *testing.T) {
	st, tc := newGatewayTestState(t)
	defer func() { _ = tc.Close() }()

	now := time.Now().UTC()
	firstID, err := st.insertInbound("default", "gatewaychannel://test", "first", nil, now)
	if err != nil {
		t.Fatalf("insert first inbound: %v", err)
	}
	secondID, err := st.insertInbound("default", "gatewaychannel://test", "second", nil, now.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("insert second inbound: %v", err)
	}

	first, err := st.claimDueInbound("default", now, time.Minute)
	if err != nil {
		t.Fatalf("claim first inbound: %v", err)
	}
	if first == nil || first.ID != firstID {
		t.Fatalf("expected first inbound id %d, got %#v", firstID, first)
	}

	if _, claimed, err := st.completeInboundSuccess("default", first.Channel, "ok", first.ID, first.Attempt, now.Add(time.Second)); err != nil {
		t.Fatalf("complete first inbound: %v", err)
	} else if !claimed {
		t.Fatalf("expected completeInboundSuccess to claim current attempt")
	}

	second, err := st.claimDueInbound("default", now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("claim second inbound after first done: %v", err)
	}
	if second == nil || second.ID != secondID {
		t.Fatalf("expected second inbound id %d, got %#v", secondID, second)
	}
}

func newGatewayTestState(t *testing.T) (*state, *lmfrt.TaskContext) {
	t.Helper()
	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("new task context: %v", err)
	}
	st := newState(tc)
	if err := st.init(); err != nil {
		_ = tc.Close()
		t.Fatalf("init gateway state: %v", err)
	}
	return st, tc
}

func querySingleString(t *testing.T, tc *lmfrt.TaskContext, query string, args ...any) string {
	t.Helper()
	res, err := tc.SqlQuery(query, args...)
	if err != nil {
		t.Fatalf("query string: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatalf("query string: no rows")
	}
	for _, v := range res.Rows[0] {
		text, _ := v.(string)
		return text
	}
	t.Fatalf("query string: no columns")
	return ""
}

func querySingleInt64(t *testing.T, tc *lmfrt.TaskContext, query string, args ...any) int64 {
	t.Helper()
	res, err := tc.SqlQuery(query, args...)
	if err != nil {
		t.Fatalf("query int64: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatalf("query int64: no rows")
	}
	for _, v := range res.Rows[0] {
		if out, ok := v.(int64); ok {
			return out
		}
		t.Fatalf("query int64: unexpected value %#v", v)
	}
	t.Fatalf("query int64: no columns")
	return 0
}
