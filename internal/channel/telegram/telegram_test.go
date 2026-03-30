package telegram

import (
	"testing"
)

func TestOwnsJID(t *testing.T) {
	tg := &Telegram{}

	tests := []struct {
		name string
		jid  string
		want bool
	}{
		{"telegram jid", "telegram:12345", true},
		{"discord jid", "discord:12345", false},
		{"empty jid", "", false},
		{"prefix only", "telegram:", true},
		{"partial prefix", "telegra", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tg.OwnsJID(tt.jid); got != tt.want {
				t.Errorf("OwnsJID(%q) = %v, want %v", tt.jid, got, tt.want)
			}
		})
	}
}

func TestChatIDFromJID(t *testing.T) {
	tests := []struct {
		name    string
		jid     string
		want    int64
		wantErr bool
	}{
		{"valid positive", "telegram:12345", 12345, false},
		{"valid negative", "telegram:-100123456", -100123456, false},
		{"invalid non-numeric", "telegram:abc", 0, true},
		{"empty after prefix", "telegram:", 0, true},
		{"no prefix", "12345", 12345, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chatIDFromJID(tt.jid)
			if (err != nil) != tt.wantErr {
				t.Errorf("chatIDFromJID(%q) error = %v, wantErr %v", tt.jid, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("chatIDFromJID(%q) = %v, want %v", tt.jid, got, tt.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	tg := &Telegram{}
	if got := tg.Name(); got != "telegram" {
		t.Errorf("Name() = %q, want %q", got, "telegram")
	}
}

func TestIsConnected(t *testing.T) {
	tg := &Telegram{}
	if tg.IsConnected() {
		t.Error("new Telegram should not be connected")
	}

	tg.connected = true
	if !tg.IsConnected() {
		t.Error("Telegram with connected=true should be connected")
	}
}
