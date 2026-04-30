package screens

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	debugTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			MarginBottom(1)

	debugLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Bold(true)

	debugDim = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// DebugPeer represents a routing table peer for the debug screen.
type DebugPeer struct {
	Address   string
	OnionAddr string
	LastSeen  int64 // Unix millis
}

// DebugConn represents a connection for the debug screen.
type DebugConn struct {
	Address string
	State   string
	Error   string
}

// Debug is the debug/diagnostics screen.
type Debug struct {
	RTSize      int
	RTPeers     []DebugPeer
	Connections []DebugConn
	OnionAddr   string
	Width       int
	Height      int
}

// NewDebug creates a new debug screen.
func NewDebug() Debug {
	return Debug{}
}

// Init returns no initial command.
func (d Debug) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (d Debug) Update(msg tea.Msg) (Debug, tea.Cmd) {
	return d, nil
}

// View renders the debug screen.
func (d Debug) View() string {
	var b strings.Builder

	b.WriteString(debugTitle.Render("Debug Info") + "\n")

	// Onion address.
	if d.OnionAddr != "" {
		b.WriteString(debugLabel.Render("onion: ") + d.OnionAddr + "\n")
	} else {
		b.WriteString(debugLabel.Render("onion: ") + debugDim.Render("not available") + "\n")
	}

	b.WriteString("\n")

	// Routing table.
	b.WriteString(debugLabel.Render(fmt.Sprintf("Routing Table (%d peers)", d.RTSize)) + "\n")
	if len(d.RTPeers) == 0 {
		b.WriteString(debugDim.Render("  (empty)") + "\n")
	} else {
		for _, p := range d.RTPeers {
			addr := truncate(p.Address, 13)
			onion := truncate(p.OnionAddr, 22)
			ago := timeSince(p.LastSeen)
			fmt.Fprintf(&b, "  %s  %s  %s\n", addr, debugDim.Render(onion), debugDim.Render(ago))
		}
	}

	b.WriteString("\n")

	// Connections.
	b.WriteString(debugLabel.Render(fmt.Sprintf("Connections (%d)", len(d.Connections))) + "\n")
	if len(d.Connections) == 0 {
		b.WriteString(debugDim.Render("  (none)") + "\n")
	} else {
		for _, c := range d.Connections {
			addr := truncate(c.Address, 13)
			line := fmt.Sprintf("  %s  %s", addr, c.State)
			if c.Error != "" {
				line += debugDim.Render(fmt.Sprintf("  err: %s", c.Error))
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(debugDim.Render("esc back") + "\n")

	return b.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	half := (maxLen - 3) / 2
	return s[:half] + "..." + s[len(s)-half:]
}

func timeSince(msTimestamp int64) string {
	if msTimestamp == 0 {
		return "never"
	}
	t := time.UnixMilli(msTimestamp)
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
