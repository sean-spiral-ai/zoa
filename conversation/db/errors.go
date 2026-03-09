package db

import "errors"

var (
	ErrNodeNotFound = errors.New("node not found")
	ErrRefNotFound  = errors.New("ref not found")
	ErrRefMoved     = errors.New("ref moved unexpectedly (CAS failure)")
	ErrRefLeased    = errors.New("ref is leased by another holder")
	ErrLeaseNotHeld = errors.New("lease not held by this holder")
)
