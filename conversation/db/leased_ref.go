package db

import (
	"fmt"
	"time"
)

func (r *LeasedRef) Name() string {
	if r == nil {
		return ""
	}
	return r.name
}

func (r *LeasedRef) RunnerID() string {
	if r == nil {
		return ""
	}
	return r.runnerID
}

func (r *LeasedRef) Hash() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hash
}

func (r *LeasedRef) LeaseDeadline() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseUntil
}

func (r *LeasedRef) LoadChain() ([]Node, error) {
	if r == nil {
		return nil, fmt.Errorf("leased ref is nil")
	}
	return r.db.LoadChain(r.Hash())
}

func (r *LeasedRef) Append(msg Message) (string, error) {
	if r == nil {
		return "", fmt.Errorf("leased ref is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return "", ErrLeaseNotHeld
	}
	newHash, err := r.db.advanceRef(r.name, r.hash, msg, r.runnerID)
	if err != nil {
		return "", err
	}
	r.hash = newHash
	return newHash, nil
}

func (r *LeasedRef) Renew() error {
	if r == nil {
		return fmt.Errorf("leased ref is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return ErrLeaseNotHeld
	}
	if err := r.db.renewLease(r.name, r.runnerID, defaultLeaseDuration); err != nil {
		return err
	}
	r.leaseUntil = time.Now().UTC().Add(defaultLeaseDuration)
	return nil
}

func (r *LeasedRef) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil
	}
	if err := r.db.releaseLease(r.name, r.runnerID); err != nil {
		return err
	}
	r.released = true
	r.leaseUntil = time.Time{}
	return nil
}
