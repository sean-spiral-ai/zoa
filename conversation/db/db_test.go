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

func TestAdvanceRefCASAndChain(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.AcquireLease("sessions/default", "runner-1", time.Minute); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}

	head, err := db.AdvanceRef("sessions/default", RootHash, Message{Role: "user", Text: "one"}, "runner-1")
	if err != nil {
		t.Fatalf("advance ref: %v", err)
	}
	if _, err := db.AdvanceRef("sessions/default", RootHash, Message{Role: "user", Text: "stale"}, "runner-1"); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("advance ref stale error = %v, want %v", err, ErrRefMoved)
	}

	ref, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	if ref.Hash != head {
		t.Fatalf("ref hash = %s, want %s", ref.Hash, head)
	}
}

func TestLeaseExpiryAndRenewal(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.AcquireLease("sessions/default", "runner-1", 20*time.Millisecond); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := db.RenewLease("sessions/default", "runner-2", time.Minute); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("renew wrong holder error = %v, want %v", err, ErrLeaseNotHeld)
	}
	time.Sleep(30 * time.Millisecond)
	if err := db.AcquireLease("sessions/default", "runner-2", time.Minute); err != nil {
		t.Fatalf("acquire after expiry: %v", err)
	}
}

func TestAcquireLeaseValidationAndMissingRef(t *testing.T) {
	db := openTestDB(t)

	if err := db.AcquireLease("sessions/default", "   ", time.Minute); err == nil {
		t.Fatal("acquire lease with blank holder unexpectedly succeeded")
	}
	if err := db.AcquireLease("sessions/missing", "runner-1", time.Minute); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("acquire missing ref error = %v, want %v", err, ErrRefNotFound)
	}
}

func TestAcquireLeaseSameHolderRefreshesExpiry(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.AcquireLease("sessions/default", "runner-1", 50*time.Millisecond); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}

	refBefore, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref before reacquire: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if err := db.AcquireLease("sessions/default", "runner-1", time.Minute); err != nil {
		t.Fatalf("reacquire lease: %v", err)
	}
	refAfter, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref after reacquire: %v", err)
	}
	if refAfter.LeasedBy != "runner-1" {
		t.Fatalf("leased by = %q, want runner-1", refAfter.LeasedBy)
	}
	if !refAfter.LeaseUntil.After(refBefore.LeaseUntil) {
		t.Fatalf("lease_until was not extended: before=%v after=%v", refBefore.LeaseUntil, refAfter.LeaseUntil)
	}
}

func TestRenewLeaseMissingRefAndSameHolderAfterExpiry(t *testing.T) {
	db := openTestDB(t)

	if err := db.RenewLease("sessions/missing", "runner-1", time.Minute); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("renew missing ref error = %v, want %v", err, ErrRefNotFound)
	}
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.AcquireLease("sessions/default", "runner-1", 20*time.Millisecond); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	if err := db.RenewLease("sessions/default", "runner-1", time.Minute); err != nil {
		t.Fatalf("renew expired lease for same holder: %v", err)
	}
	ref, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	if ref.LeasedBy != "runner-1" {
		t.Fatalf("leased by = %q, want runner-1", ref.LeasedBy)
	}
	if !ref.LeaseUntil.After(time.Now().UTC()) {
		t.Fatalf("lease_until = %v, want future time", ref.LeaseUntil)
	}
}

func TestReleaseLeaseClearsHolderAndIgnoresNonHolder(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.AcquireLease("sessions/default", "runner-1", time.Minute); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}

	if err := db.ReleaseLease("sessions/default", "runner-2"); err != nil {
		t.Fatalf("release lease by non-holder: %v", err)
	}
	ref, err := db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref after non-holder release: %v", err)
	}
	if ref.LeasedBy != "runner-1" {
		t.Fatalf("leased by after non-holder release = %q, want runner-1", ref.LeasedBy)
	}

	if err := db.ReleaseLease("sessions/default", "runner-1"); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	ref, err = db.GetRef("sessions/default")
	if err != nil {
		t.Fatalf("get ref after release: %v", err)
	}
	if ref.LeasedBy != "" {
		t.Fatalf("leased by after release = %q, want empty", ref.LeasedBy)
	}
	if !ref.LeaseUntil.IsZero() {
		t.Fatalf("lease_until after release = %v, want zero", ref.LeaseUntil)
	}
	if err := db.AcquireLease("sessions/default", "runner-2", time.Minute); err != nil {
		t.Fatalf("acquire lease after release: %v", err)
	}
}

func TestAdvanceRefRejectsOtherLeaseHolder(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.AcquireLease("sessions/default", "runner-1", time.Minute); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if _, err := db.AdvanceRef("sessions/default", RootHash, Message{Role: "user", Text: "blocked"}, "runner-2"); !errors.Is(err, ErrRefLeased) {
		t.Fatalf("advance ref error = %v, want %v", err, ErrRefLeased)
	}
}

func TestSetRefAndMissingNode(t *testing.T) {
	db := openTestDB(t)
	if err := db.CreateRef("sessions/default", RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
	if err := db.SetRef("sessions/default", "missing"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("set ref error = %v, want %v", err, ErrNodeNotFound)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "conversation.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
