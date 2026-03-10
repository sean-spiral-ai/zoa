package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (db *DB) LeaseRef(name string, runnerID string, duration time.Duration) (*LeasedRef, error) {
	if err := db.acquireLease(name, runnerID, duration); err != nil {
		return nil, err
	}
	ref, err := db.GetRef(name)
	if err != nil {
		_ = db.releaseLease(name, runnerID)
		return nil, err
	}
	return &LeasedRef{
		db:            db,
		name:          ref.Name,
		runnerID:      strings.TrimSpace(runnerID),
		leaseDuration: duration,
		hash:          ref.Hash,
		leaseUntil:    ref.LeaseUntil,
	}, nil
}

func (db *DB) acquireLease(name string, runnerID string, duration time.Duration) error {
	if strings.TrimSpace(runnerID) == "" {
		return fmt.Errorf("runner id is required")
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin acquire lease: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	ref, leaseExpired, err := loadRefForUpdate(tx, name)
	if err != nil {
		return err
	}
	if ref.LeasedBy != "" && ref.LeasedBy != runnerID && !leaseExpired {
		return ErrRefLeased
	}
	if _, err := tx.Exec(
		`UPDATE conversation_ref
		 SET leased_by = ?, lease_until = ?, updated_at = ?
		 WHERE name = ?`,
		runnerID,
		time.Now().UTC().Add(duration).Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		name,
	); err != nil {
		return fmt.Errorf("acquire lease: %w", err)
	}
	return tx.Commit()
}

func (db *DB) renewLease(name string, runnerID string, duration time.Duration) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin renew lease: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	ref, _, err := loadRefForUpdate(tx, name)
	if err != nil {
		return err
	}
	if ref.LeasedBy != runnerID {
		return ErrLeaseNotHeld
	}
	if _, err := tx.Exec(
		`UPDATE conversation_ref
		 SET lease_until = ?, updated_at = ?
		 WHERE name = ?`,
		time.Now().UTC().Add(duration).Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		name,
	); err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	return tx.Commit()
}

func (db *DB) releaseLease(name string, runnerID string) error {
	res, err := db.sql.Exec(
		`UPDATE conversation_ref
		 SET leased_by = '', lease_until = '', updated_at = ?
		 WHERE name = ? AND leased_by = ?`,
		time.Now().UTC().Format(time.RFC3339Nano),
		name,
		runnerID,
	)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	_, _ = res.RowsAffected()
	return nil
}

func loadRefForUpdate(tx *sql.Tx, name string) (RefSnapshot, bool, error) {
	var (
		ref            RefSnapshot
		leaseUntilText string
	)
	err := tx.QueryRow(
		`SELECT name, hash, leased_by, lease_until FROM conversation_ref WHERE name = ?`,
		strings.TrimSpace(name),
	).Scan(&ref.Name, &ref.Hash, &ref.LeasedBy, &leaseUntilText)
	if err == sql.ErrNoRows {
		return RefSnapshot{}, false, ErrRefNotFound
	}
	if err != nil {
		return RefSnapshot{}, false, fmt.Errorf("load ref: %w", err)
	}
	ref.LeaseUntil = parseLeaseUntil(leaseUntilText)
	return ref, leaseExpired(ref.LeaseUntil), nil
}

func parseLeaseUntil(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func leaseExpired(until time.Time) bool {
	return !until.IsZero() && !until.After(time.Now().UTC())
}
