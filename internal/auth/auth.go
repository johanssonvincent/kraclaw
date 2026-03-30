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

// IsAllowed checks if a sender is permitted in the given chat.
// If no allowlist entries exist for the chat, all senders are allowed.
// If entries exist, the sender must match an entry with mode "trigger".
// Entries with mode "drop" cause the message to be dropped.
// A pattern of "*" matches all senders.
func (a *Authorizer) IsAllowed(ctx context.Context, chatJID string, sender string) (bool, error) {
	entries, err := a.store.GetAllowlist(ctx, chatJID)
	if err != nil {
		return false, err
	}

	if len(entries) == 0 {
		return true, nil
	}

	for _, e := range entries {
		matches := e.AllowPattern == "*" || e.AllowPattern == sender
		if matches {
			return e.Mode == store.ModeTrigger, nil
		}
	}

	// Sender not in allowlist — deny by default.
	return false, nil
}
