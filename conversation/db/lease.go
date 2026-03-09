package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (db *DB) AcquireLease(name string, leaseHolder string, duration time.Duration) error {
	if strings.TrimSpace(leaseHolder) == "" {
		return fmt.Errorf("lease holder is required")
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
	if ref.LeasedBy != "" && ref.LeasedBy != leaseHolder && !leaseExpired {
		return ErrRefLeased
	}
	if _, err := tx.Exec(
		`UPDATE conversation_ref
		 SET leased_by = ?, lease_until = ?, updated_at = ?
		 WHERE name = ?`,
		leaseHolder,
		time.Now().UTC().Add(duration).Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		name,
	); err != nil {
		return fmt.Errorf("acquire lease: %w", err)
	}
	return tx.Commit()
}

func (db *DB) RenewLease(name string, leaseHolder string, duration time.Duration) error {
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
	if ref.LeasedBy != leaseHolder {
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

func (db *DB) ReleaseLease(name string, leaseHolder string) error {
	res, err := db.sql.Exec(
		`UPDATE conversation_ref
		 SET leased_by = '', lease_until = '', updated_at = ?
		 WHERE name = ? AND leased_by = ?`,
		time.Now().UTC().Format(time.RFC3339Nano),
		name,
		leaseHolder,
	)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	_, _ = res.RowsAffected()
	return nil
}

func loadRefForUpdate(tx *sql.Tx, name string) (Ref, bool, error) {
	var (
		ref            Ref
		leaseUntilText string
	)
	err := tx.QueryRow(
		`SELECT name, hash, leased_by, lease_until FROM conversation_ref WHERE name = ?`,
		strings.TrimSpace(name),
	).Scan(&ref.Name, &ref.Hash, &ref.LeasedBy, &leaseUntilText)
	if err == sql.ErrNoRows {
		return Ref{}, false, ErrRefNotFound
	}
	if err != nil {
		return Ref{}, false, fmt.Errorf("load ref: %w", err)
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
