package components

import (
	"strings"
	"testing"
)

func TestStatusBar_DHTDisplay(t *testing.T) {
	sb := NewStatusBar()
	sb.Width = 120
	sb.Address = "ETestAddr123456789012345678901234"
	sb.TorState = "ready"
	sb.PeerCount = 3
	sb.RTSize = 7

	view := sb.View()
	if !strings.Contains(view, "dht:7") {
		t.Fatalf("status bar should contain 'dht:7', got: %s", view)
	}
	if !strings.Contains(view, "peers:3") {
		t.Fatalf("status bar should contain 'peers:3', got: %s", view)
	}
}

func TestStatusBar_DHTZero(t *testing.T) {
	sb := NewStatusBar()
	sb.Width = 120
	sb.Address = "ETestAddr123456789012345678901234"
	sb.RTSize = 0

	view := sb.View()
	if !strings.Contains(view, "dht:0") {
		t.Fatalf("status bar should contain 'dht:0', got: %s", view)
	}
}

func TestStatusBar_TorColors(t *testing.T) {
	tests := []struct {
		state string
		label string
	}{
		{"ready", "tor:ready"},
		{"bootstrapping", "tor:bootstrapping"},
		{"disabled", "tor:disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			sb := NewStatusBar()
			sb.Width = 120
			sb.TorState = tt.state
			view := sb.View()
			if !strings.Contains(view, tt.label) {
				t.Fatalf("status bar should contain %q, got: %s", tt.label, view)
			}
		})
	}
}

func TestStatusBar_AddressTruncation(t *testing.T) {
	sb := NewStatusBar()
	sb.Width = 120
	sb.Address = "ELongAddressWithManyCharacters12345"

	view := sb.View()
	// Long addresses should be truncated with "..."
	if !strings.Contains(view, "...") {
		t.Fatalf("long address should be truncated, got: %s", view)
	}
}

func TestStatusBar_ShortAddress(t *testing.T) {
	sb := NewStatusBar()
	sb.Width = 120
	sb.Address = "EShort"

	view := sb.View()
	if !strings.Contains(view, "EShort") {
		t.Fatalf("short address should not be truncated, got: %s", view)
	}
}
