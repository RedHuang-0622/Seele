package accountpool

import "errors"

var (
	// ErrInvalidAccount indicates that an account registration is incomplete.
	ErrInvalidAccount = errors.New("accountpool: invalid account")
	// ErrAccountExists indicates that an account ID is already registered.
	ErrAccountExists = errors.New("accountpool: account already exists")
	// ErrAccountNotFound indicates that the requested account ID is unknown.
	ErrAccountNotFound = errors.New("accountpool: account not found")
	// ErrAccountDisabled indicates that a pinned account cannot accept new leases.
	ErrAccountDisabled = errors.New("accountpool: account disabled")
	// ErrAccountBusy indicates that an account still owns active leases.
	ErrAccountBusy = errors.New("accountpool: account has active leases")
	// ErrNoEligibleAccount indicates that no enabled account matches a request.
	ErrNoEligibleAccount = errors.New("accountpool: no eligible account")
	// ErrInvalidSelection indicates that a custom selector returned an unknown candidate.
	ErrInvalidSelection = errors.New("accountpool: selector returned an invalid candidate")
)
