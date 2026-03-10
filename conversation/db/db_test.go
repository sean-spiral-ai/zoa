package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndLoadChain(t *testing.T) {
	db := openTestDB(t)

	systemHash, err := db.Append(RootHash, Message{Role: "system", Text: "sys"})
	if err != nil {
		t.Fatalf("append system: %v", err)
	}
	userHash, err := db.Append(systemHash, Message{Role: "user", Text: "hello"})
	if err != nil {
		t.Fatalf("append user: %v", err)
	}

	chain, err := db.LoadChain(userHash)
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want 2", len(chain))
	}
	if chain[0].Message.Text != "sys" || chain[1].Message.Text != "hello" {
		t.Fatalf("unexpected chain order: %#v", chain)
	}
}

func TestAppendIdempotent(t *testing.T) {
	db := openTestDB(t)
	msg := Message{Role: "user", Text: "same"}

	h1, err := db.Append(RootHash, msg)
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	h2, err := db.Append(RootHash, msg)
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash mismatch: %s vs %s", h1, h2)
	}
}

func TestLeasedRefAppendAndChain(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	ref, err := db.LeaseRef("sessions/default", "runner-1")
	if err != nil {
		t.Fatalf("lease ref: %v", err)
	}
	defer func() { _ = ref.Close() }()

	head, err := ref.Append(Message{Role: "user", Text: "one"})
	if err != nil {
		t.Fatalf("append through leased ref: %v", err)
	}

	snapshot, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	if snapshot.Hash != head {
		t.Fatalf("ref hash = %s, want %s", snapshot.Hash, head)
	}

	chain, err := ref.LoadChain()
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	if len(chain) != 1 || chain[0].Message.Text != "one" {
		t.Fatalf("unexpected chain: %#v", chain)
	}
}

func TestLeaseRefExpiryAndRenewal(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.acquireLease("sessions/default", "runner-1", 20*time.Millisecond); err != nil {
		t.Fatalf("acquire short lease: %v", err)
	}
	snapshot, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	ref1 := &LeasedRef{
		db:         db,
		name:       snapshot.Name,
		runnerID:   "runner-1",
		hash:       snapshot.Hash,
		leaseUntil: snapshot.LeaseUntil,
	}
	if err := db.renewLease("sessions/default", "runner-2", time.Minute); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("renew wrong holder error = %v, want %v", err, ErrLeaseNotHeld)
	}
	time.Sleep(30 * time.Millisecond)
	ref2, err := db.LeaseRef("sessions/default", "runner-2")
	if err != nil {
		t.Fatalf("lease after expiry: %v", err)
	}
	defer func() { _ = ref2.Close() }()
	if _, err := ref1.Append(Message{Role: "user", Text: "stale"}); !errors.Is(err, ErrRefLeased) && !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale append error = %v, want %v or %v", err, ErrRefLeased, ErrLeaseNotHeld)
	}
}

func TestLeaseRefValidationAndMissingRef(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.LeaseRef("sessions/default", "   "); err == nil {
		t.Fatal("lease ref with blank runner id unexpectedly succeeded")
	}
	if _, err := db.LeaseRef("sessions/missing", "runner-1"); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("lease missing ref error = %v, want %v", err, ErrRefNotFound)
	}
}

func TestLeaseRefSameHolderRefreshesExpiry(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.acquireLease("sessions/default", "runner-1", 50*time.Millisecond); err != nil {
		t.Fatalf("acquire short lease: %v", err)
	}
	ref, err := db.LeaseRef("sessions/default", "runner-1")
	if err != nil {
		t.Fatalf("lease ref: %v", err)
	}
	defer func() { _ = ref.Close() }()

	before, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref before reacquire: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	ref2, err := db.LeaseRef("sessions/default", "runner-1")
	if err != nil {
		t.Fatalf("reacquire lease ref: %v", err)
	}
	defer func() { _ = ref2.Close() }()

	after, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref after reacquire: %v", err)
	}
	if after.LeasedBy != "runner-1" {
		t.Fatalf("leased by = %q, want runner-1", after.LeasedBy)
	}
	if !after.LeaseUntil.After(before.LeaseUntil) {
		t.Fatalf("lease_until was not extended: before=%v after=%v", before.LeaseUntil, after.LeaseUntil)
	}
}

func TestRenewLeaseMissingRefAndSameHolderAfterExpiry(t *testing.T) {
	db := openTestDB(t)

	if err := db.renewLease("sessions/missing", "runner-1", time.Minute); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("renew missing ref error = %v, want %v", err, ErrRefNotFound)
	}
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.acquireLease("sessions/default", "runner-1", 20*time.Millisecond); err != nil {
		t.Fatalf("acquire short lease: %v", err)
	}
	snapshot, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	ref := &LeasedRef{
		db:         db,
		name:       snapshot.Name,
		runnerID:   "runner-1",
		hash:       snapshot.Hash,
		leaseUntil: snapshot.LeaseUntil,
	}
	defer func() { _ = ref.Close() }()
	time.Sleep(30 * time.Millisecond)

	if err := ref.Renew(); err != nil {
		t.Fatalf("renew expired lease for same holder: %v", err)
	}
	snapshot, err = db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	if snapshot.LeasedBy != "runner-1" {
		t.Fatalf("leased by = %q, want runner-1", snapshot.LeasedBy)
	}
	if !snapshot.LeaseUntil.After(time.Now().UTC()) {
		t.Fatalf("lease_until = %v, want future time", snapshot.LeaseUntil)
	}
}

func TestReleaseLeaseClearsHolderAndIgnoresNonHolder(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	ref, err := db.LeaseRef("sessions/default", "runner-1")
	if err != nil {
		t.Fatalf("lease ref: %v", err)
	}

	if err := db.releaseLease("sessions/default", "runner-2"); err != nil {
		t.Fatalf("release lease by non-holder: %v", err)
	}
	snapshot, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref after non-holder release: %v", err)
	}
	if snapshot.LeasedBy != "runner-1" {
		t.Fatalf("leased by after non-holder release = %q, want runner-1", snapshot.LeasedBy)
	}

	if err := ref.Close(); err != nil {
		t.Fatalf("close leased ref: %v", err)
	}
	snapshot, err = db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref after release: %v", err)
	}
	if snapshot.LeasedBy != "" {
		t.Fatalf("leased by after release = %q, want empty", snapshot.LeasedBy)
	}
	if !snapshot.LeaseUntil.IsZero() {
		t.Fatalf("lease_until after release = %v, want zero", snapshot.LeaseUntil)
	}
	ref2, err := db.LeaseRef("sessions/default", "runner-2")
	if err != nil {
		t.Fatalf("lease ref after release: %v", err)
	}
	defer func() { _ = ref2.Close() }()
}

func TestLeaseRefRejectsOtherHolder(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	ref, err := db.LeaseRef("sessions/default", "runner-1")
	if err != nil {
		t.Fatalf("lease ref: %v", err)
	}
	defer func() { _ = ref.Close() }()

	if _, err := db.LeaseRef("sessions/default", "runner-2"); !errors.Is(err, ErrRefLeased) {
		t.Fatalf("lease ref error = %v, want %v", err, ErrRefLeased)
	}
}

func TestListRefsReturnsSortedRefsWithLeaseState(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create session ref: %v", err)
	}
	if err := db.CreateRef("tasks/task-2/main", RootHash); err != nil {
		t.Fatalf("create task ref: %v", err)
	}
	ref, err := db.LeaseRef("tasks/task-2/main", "runner-1")
	if err != nil {
		t.Fatalf("lease ref: %v", err)
	}
	defer func() { _ = ref.Close() }()

	refs, err := db.ListRefs()
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("ref count = %d, want 2", len(refs))
	}
	if refs[0].Name != "sessions/default" {
		t.Fatalf("first ref = %q, want sessions/default", refs[0].Name)
	}
	if refs[1].Name != "tasks/task-2/main" {
		t.Fatalf("second ref = %q, want tasks/task-2/main", refs[1].Name)
	}
	if refs[1].LeasedBy != "runner-1" {
		t.Fatalf("leased by = %q, want runner-1", refs[1].LeasedBy)
	}
	if !refs[1].LeaseUntil.After(time.Now().UTC()) {
		t.Fatalf("lease_until = %v, want future time", refs[1].LeaseUntil)
	}
}

func TestCreateRefRejectsMissingNode(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", "missing"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("create ref error = %v, want %v", err, ErrNodeNotFound)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "conversation.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
