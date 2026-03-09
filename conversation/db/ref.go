package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (db *DB) GetRef(name string) (Ref, error) {
	var (
		ref            Ref
		leaseUntilText string
	)
	err := db.sql.QueryRow(
		`SELECT name, hash, leased_by, lease_until FROM conversation_ref WHERE name = ?`,
		strings.TrimSpace(name),
	).Scan(&ref.Name, &ref.Hash, &ref.LeasedBy, &leaseUntilText)
	if err == sql.ErrNoRows {
		return Ref{}, ErrRefNotFound
	}
	if err != nil {
		return Ref{}, fmt.Errorf("get ref: %w", err)
	}
	ref.LeaseUntil = parseLeaseUntil(leaseUntilText)
	return ref, nil
}

func (db *DB) CreateRef(name string, hash string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("ref name is required")
	}
	if hash == "" {
		hash = RootHash
	}
	if err := db.ensureNodeExists(hash); err != nil {
		return err
	}
	_, err := db.sql.Exec(
		`INSERT INTO conversation_ref (name, hash, leased_by, lease_until, updated_at)
		 VALUES (?, ?, '', '', ?)`,
		name, hash, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("create ref: %w", err)
	}
	return nil
}

func (db *DB) SetRef(name string, hash string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("ref name is required")
	}
	if hash == "" {
		hash = RootHash
	}
	if err := db.ensureNodeExists(hash); err != nil {
		return err
	}
	res, err := db.sql.Exec(
		`UPDATE conversation_ref SET hash = ?, updated_at = ? WHERE name = ?`,
		hash, time.Now().UTC().Format(time.RFC3339Nano), name,
	)
	if err != nil {
		return fmt.Errorf("set ref: %w", err)
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return ErrRefNotFound
	}
	return nil
}

func (db *DB) AdvanceRef(name string, expectHash string, msg Message, leaseHolder string) (string, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return "", fmt.Errorf("begin advance ref: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	ref, leaseExpired, err := loadRefForUpdate(tx, name)
	if err != nil {
		return "", err
	}
	if ref.Hash != expectHash {
		return "", ErrRefMoved
	}
	if ref.LeasedBy != "" && ref.LeasedBy != leaseHolder && !leaseExpired {
		return "", ErrRefLeased
	}
	newHash, err := db.insertNode(tx, expectHash, msg)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`UPDATE conversation_ref
		 SET hash = ?, updated_at = ?
		 WHERE name = ? AND hash = ?`,
		newHash, time.Now().UTC().Format(time.RFC3339Nano), name, expectHash,
	); err != nil {
		return "", fmt.Errorf("advance ref: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit advance ref: %w", err)
	}
	return newHash, nil
}
