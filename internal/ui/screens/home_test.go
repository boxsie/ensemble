package screens

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyMsg creates a tea.KeyMsg for testing.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func TestHome_View_Empty(t *testing.T) {
	h := NewHome()
	view := h.View()

	if !strings.Contains(view, "No contacts yet") {
		t.Fatal("empty home should show 'No contacts yet'")
	}
}

func TestHome_View_WithContacts(t *testing.T) {
	h := NewHome()
	h.Contacts = []ContactItem{
		{Address: "EAddr1", Alias: "Alice", Online: true},
		{Address: "EAddr2", Alias: "Bob", Online: false},
	}

	view := h.View()

	if !strings.Contains(view, "Alice") {
		t.Fatal("should display Alice")
	}
	if !strings.Contains(view, "Bob") {
		t.Fatal("should display Bob")
	}
	if !strings.Contains(view, "online") {
		t.Fatal("should show online status")
	}
	if !strings.Contains(view, "offline") {
		t.Fatal("should show offline status")
	}
}

func TestHome_View_StatusField(t *testing.T) {
	h := NewHome()
	h.Contacts = []ContactItem{
		{Address: "EAddr1", Alias: "Alice", Status: "discovering..."},
		{Address: "EAddr2", Alias: "Bob", Online: true},
	}

	view := h.View()

	// Status field should override online/offline display.
	if !strings.Contains(view, "discovering...") {
		t.Fatal("should show Status field when set")
	}
	// Bob has no Status set, so should show online.
	if !strings.Contains(view, "online") {
		t.Fatal("should show online for Bob (no Status field)")
	}
}

func TestHome_View_StatusOverridesOnline(t *testing.T) {
	h := NewHome()
	h.Contacts = []ContactItem{
		{Address: "EAddr1", Alias: "Alice", Online: true, Status: "signaling..."},
	}

	view := h.View()

	if !strings.Contains(view, "signaling...") {
		t.Fatal("Status field should take priority over online")
	}
}

func TestHome_View_DebugKeyHint(t *testing.T) {
	h := NewHome()
	view := h.View()

	if !strings.Contains(view, "debug") {
		t.Fatal("should show debug key hint")
	}
}

func TestHome_CursorNavigation(t *testing.T) {
	h := NewHome()
	h.Contacts = []ContactItem{
		{Address: "E1", Alias: "A"},
		{Address: "E2", Alias: "B"},
		{Address: "E3", Alias: "C"},
	}

	if h.Cursor != 0 {
		t.Fatal("initial cursor should be 0")
	}

	// Can't go above 0.
	h, _ = h.Update(keyMsg("up"))
	if h.Cursor != 0 {
		t.Fatal("cursor should stay at 0")
	}

	// Move down.
	h, _ = h.Update(keyMsg("down"))
	if h.Cursor != 1 {
		t.Fatalf("cursor should be 1, got %d", h.Cursor)
	}

	h, _ = h.Update(keyMsg("down"))
	if h.Cursor != 2 {
		t.Fatalf("cursor should be 2, got %d", h.Cursor)
	}

	// Can't go beyond last.
	h, _ = h.Update(keyMsg("down"))
	if h.Cursor != 2 {
		t.Fatal("cursor should stay at 2")
	}
}
