package auth

import (
	"context"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

// Authorizer checks whether a sender is allowed to interact with a chat.
type Authorizer struct {
	store store.AllowlistStore
}

// New creates a new Authorizer.
func New(store store.AllowlistStore) *Authorizer {
	return &Authorizer{store: store}
}

// wildcardPattern matches every sender in a chat's allowlist.
const wildcardPattern = "*"

// IsAllowed checks if a sender is permitted in the given chat.
//
// Rules are resolved by specificity, not by row order: GetAllowlist issues a
// query without ORDER BY, so a first-match-wins scan made the verdict depend on
// storage order (an exact drop rule for one sender was silently overridden
// whenever the "*" rule came back first). An exact rule for this sender beats
// the "*" catch-all, and within the winning specificity any drop rule denies.
func (a *Authorizer) IsAllowed(ctx context.Context, chatJID string, sender string) (bool, error) {
	entries, err := a.store.GetAllowlist(ctx, chatJID)
	if err != nil {
		return false, err
	}

	if len(entries) == 0 {
		return true, nil
	}

	var exactAllow, exactDeny, wildcardAllow, wildcardDeny bool

	for _, e := range entries {
		allows := e.Mode == store.ModeTrigger

		switch e.AllowPattern {
		case sender:
			if allows {
				exactAllow = true
			} else {
				exactDeny = true
			}
		case wildcardPattern:
			if allows {
				wildcardAllow = true
			} else {
				wildcardDeny = true
			}
		}
	}

	switch {
	case exactDeny:
		return false, nil
	case exactAllow:
		return true, nil
	case wildcardDeny:
		return false, nil
	case wildcardAllow:
		return true, nil
	default:
		// Sender not in allowlist — deny by default.
		return false, nil
	}
}
