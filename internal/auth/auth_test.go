package auth

import (
	"context"
	"fmt"
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

type mockAllowlistStore struct {
	entries map[string][]store.SenderAllowlistEntry
}

func (m *mockAllowlistStore) GetAllowlist(_ context.Context, chatJID string) ([]store.SenderAllowlistEntry, error) {
	return m.entries[chatJID], nil
}

func (m *mockAllowlistStore) UpsertAllowlistEntry(_ context.Context, _ *store.SenderAllowlistEntry) error {
	return nil
}

func (m *mockAllowlistStore) DeleteAllowlistEntry(_ context.Context, _ int64) error {
	return nil
}

type errorAllowlistStore struct{}

func (e *errorAllowlistStore) GetAllowlist(_ context.Context, _ string) ([]store.SenderAllowlistEntry, error) {
	return nil, fmt.Errorf("db error")
}

func (e *errorAllowlistStore) UpsertAllowlistEntry(_ context.Context, _ *store.SenderAllowlistEntry) error {
	return nil
}

func (e *errorAllowlistStore) DeleteAllowlistEntry(_ context.Context, _ int64) error {
	return nil
}

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name    string
		entries map[string][]store.SenderAllowlistEntry
		chatJID string
		sender  string
		want    bool
		wantErr bool
	}{
		{
			name:    "no entries allows all",
			entries: map[string][]store.SenderAllowlistEntry{},
			chatJID: "chat1",
			sender:  "anyone",
			want:    true,
		},
		{
			name: "wildcard trigger allows all",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {{AllowPattern: "*", Mode: store.ModeTrigger}},
			},
			chatJID: "chat1",
			sender:  "anyone",
			want:    true,
		},
		{
			name: "wildcard drop denies all",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {{AllowPattern: "*", Mode: store.ModeDrop}},
			},
			chatJID: "chat1",
			sender:  "anyone",
			want:    false,
		},
		{
			name: "exact match trigger",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeTrigger}},
			},
			chatJID: "chat1",
			sender:  "alice@s.whatsapp.net",
			want:    true,
		},
		{
			name: "exact match drop",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeDrop}},
			},
			chatJID: "chat1",
			sender:  "alice@s.whatsapp.net",
			want:    false,
		},
		{
			name: "sender not in allowlist",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeTrigger}},
			},
			chatJID: "chat1",
			sender:  "bob@s.whatsapp.net",
			want:    false,
		},
		{
			name: "different chat has no entries",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeTrigger}},
			},
			chatJID: "chat2",
			sender:  "anyone",
			want:    true,
		},
		{
			name: "wildcard trigger plus exact drop denies that sender",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {
					{AllowPattern: "*", Mode: store.ModeTrigger},
					{AllowPattern: "bob@s.whatsapp.net", Mode: store.ModeDrop},
				},
			},
			chatJID: "chat1",
			sender:  "bob@s.whatsapp.net",
			want:    false,
		},
		{
			name: "wildcard trigger plus exact drop allows other senders",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {
					{AllowPattern: "*", Mode: store.ModeTrigger},
					{AllowPattern: "bob@s.whatsapp.net", Mode: store.ModeDrop},
				},
			},
			chatJID: "chat1",
			sender:  "alice@s.whatsapp.net",
			want:    true,
		},
		{
			name: "wildcard drop plus exact trigger allows that sender",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {
					{AllowPattern: "*", Mode: store.ModeDrop},
					{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeTrigger},
				},
			},
			chatJID: "chat1",
			sender:  "alice@s.whatsapp.net",
			want:    true,
		},
		{
			name: "duplicate exact rules deny",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {
					{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeTrigger},
					{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeDrop},
				},
			},
			chatJID: "chat1",
			sender:  "alice@s.whatsapp.net",
			want:    false,
		},
		{
			name: "duplicate wildcard rules deny",
			entries: map[string][]store.SenderAllowlistEntry{
				"chat1": {
					{AllowPattern: "*", Mode: store.ModeTrigger},
					{AllowPattern: "*", Mode: store.ModeDrop},
				},
			},
			chatJID: "chat1",
			sender:  "alice@s.whatsapp.net",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(&mockAllowlistStore{entries: tt.entries})
			got, err := a.IsAllowed(context.Background(), tt.chatJID, tt.sender)
			if (err != nil) != tt.wantErr {
				t.Fatalf("IsAllowed() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("IsAllowed(%q, %q) = %v, want %v", tt.chatJID, tt.sender, got, tt.want)
			}
		})
	}
}

func TestIsAllowed_StoreError(t *testing.T) {
	a := New(&errorAllowlistStore{})
	_, err := a.IsAllowed(context.Background(), "chat1", "sender")
	if err == nil {
		t.Error("IsAllowed() expected error, got nil")
	}
}

// TestIsAllowed_OrderIndependent pins the verdict to the rule set rather than to
// the order MySQL happened to return rows in: GetAllowlist has no ORDER BY, so a
// first-match-wins loop made "allow everyone except bob" depend on storage order.
func TestIsAllowed_OrderIndependent(t *testing.T) {
	rules := []store.SenderAllowlistEntry{
		{AllowPattern: "*", Mode: store.ModeTrigger},
		{AllowPattern: "bob@s.whatsapp.net", Mode: store.ModeDrop},
		{AllowPattern: "alice@s.whatsapp.net", Mode: store.ModeTrigger},
	}

	want := map[string]bool{
		"alice@s.whatsapp.net": true,
		"bob@s.whatsapp.net":   false,
		"carol@s.whatsapp.net": true,
	}

	for _, order := range permutations(rules) {
		a := New(&mockAllowlistStore{entries: map[string][]store.SenderAllowlistEntry{"chat1": order}})

		for sender, expected := range want {
			got, err := a.IsAllowed(context.Background(), "chat1", sender)
			if err != nil {
				t.Fatalf("IsAllowed(%q) error = %v", sender, err)
			}

			if got != expected {
				t.Errorf("IsAllowed(%q) with rule order %v = %v, want %v", sender, patterns(order), got, expected)
			}
		}
	}
}

// permutations returns every ordering of the given rules (3 rules => 6 orders).
func permutations(rules []store.SenderAllowlistEntry) [][]store.SenderAllowlistEntry {
	if len(rules) <= 1 {
		return [][]store.SenderAllowlistEntry{rules}
	}

	var out [][]store.SenderAllowlistEntry

	for i := range rules {
		rest := make([]store.SenderAllowlistEntry, 0, len(rules)-1)

		for j := range rules {
			if j != i {
				rest = append(rest, rules[j])
			}
		}

		for _, tail := range permutations(rest) {
			out = append(out, append([]store.SenderAllowlistEntry{rules[i]}, tail...))
		}
	}

	return out
}

func patterns(rules []store.SenderAllowlistEntry) []string {
	out := make([]string, 0, len(rules))

	for _, r := range rules {
		out = append(out, r.AllowPattern+":"+string(r.Mode))
	}

	return out
}
