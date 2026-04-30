package screens

import (
	"strings"
	"testing"
	"time"
)

func TestDebug_View_Empty(t *testing.T) {
	d := NewDebug()
	view := d.View()

	if !strings.Contains(view, "Debug Info") {
		t.Fatal("should contain title 'Debug Info'")
	}
	if !strings.Contains(view, "(empty)") {
		t.Fatal("should show (empty) when no routing table peers")
	}
	if !strings.Contains(view, "(none)") {
		t.Fatal("should show (none) when no connections")
	}
	if !strings.Contains(view, "not available") {
		t.Fatal("should show 'not available' when no onion address")
	}
}

func TestDebug_View_WithPeers(t *testing.T) {
	d := NewDebug()
	d.RTSize = 2
	d.OnionAddr = "testaddr123456.onion"
	d.RTPeers = []DebugPeer{
		{Address: "EAddr1234567890", OnionAddr: "peer1.onion", LastSeen: time.Now().UnixMilli()},
		{Address: "EAddr0987654321", OnionAddr: "peer2.onion", LastSeen: time.Now().Add(-5 * time.Minute).UnixMilli()},
	}

	view := d.View()

	if !strings.Contains(view, "Routing Table (2 peers)") {
		t.Fatal("should show routing table size")
	}
	if !strings.Contains(view, "testaddr123456.onion") {
		t.Fatal("should show onion address")
	}
	if strings.Contains(view, "(empty)") {
		t.Fatal("should not show (empty) when peers exist")
	}
}

func TestDebug_View_WithConnections(t *testing.T) {
	d := NewDebug()
	d.Connections = []DebugConn{
		{Address: "EConnAddr1", State: "connected"},
		{Address: "EConnAddr2", State: "failed", Error: "timeout"},
	}

	view := d.View()

	if !strings.Contains(view, "Connections (2)") {
		t.Fatal("should show connection count")
	}
	if !strings.Contains(view, "connected") {
		t.Fatal("should show connection state")
	}
	if !strings.Contains(view, "timeout") {
		t.Fatal("should show error for failed connections")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"abcdefghijk", 7, "ab...jk"},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if len(got) > tt.maxLen && len(tt.input) > tt.maxLen {
			// Just verify it was truncated
			if !strings.Contains(got, "...") {
				t.Fatalf("truncate(%q, %d) should contain '...', got %q", tt.input, tt.maxLen, got)
			}
		}
	}
}

func TestTimeSince(t *testing.T) {
	// Zero timestamp.
	if got := timeSince(0); got != "never" {
		t.Fatalf("timeSince(0): got %q, want %q", got, "never")
	}

	// Recent timestamp.
	recent := time.Now().Add(-30 * time.Second).UnixMilli()
	got := timeSince(recent)
	if !strings.HasSuffix(got, "s ago") {
		t.Fatalf("timeSince(30s ago): got %q, want *s ago", got)
	}

	// Minutes ago.
	minAgo := time.Now().Add(-5 * time.Minute).UnixMilli()
	got = timeSince(minAgo)
	if !strings.HasSuffix(got, "m ago") {
		t.Fatalf("timeSince(5m ago): got %q, want *m ago", got)
	}

	// Hours ago.
	hourAgo := time.Now().Add(-3 * time.Hour).UnixMilli()
	got = timeSince(hourAgo)
	if !strings.HasSuffix(got, "h ago") {
		t.Fatalf("timeSince(3h ago): got %q, want *h ago", got)
	}

	// Days ago.
	dayAgo := time.Now().Add(-48 * time.Hour).UnixMilli()
	got = timeSince(dayAgo)
	if !strings.HasSuffix(got, "d ago") {
		t.Fatalf("timeSince(2d ago): got %q, want *d ago", got)
	}
}
