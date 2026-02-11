package components

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var statusBarStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#1F2937")).
	Foreground(lipgloss.Color("#E5E7EB")).
	Padding(0, 1)

var torLabelStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#1F2937")).
	Bold(true)

// StatusBar displays node status at the bottom of the screen.
type StatusBar struct {
	Address   string
	TorState  string
	OnionAddr string
	PeerCount int32
	Width     int
}

// NewStatusBar creates a new status bar.
func NewStatusBar() StatusBar {
	return StatusBar{
		TorState: "disabled",
	}
}

// Update handles messages for the status bar.
func (s StatusBar) Update(_ tea.Msg) StatusBar {
	return s
}

// View renders the status bar.
func (s StatusBar) View() string {
	addr := s.Address
	if len(addr) > 16 {
		addr = addr[:6] + "..." + addr[len(addr)-4:]
	}

	var torColor lipgloss.Color
	switch s.TorState {
	case "ready":
		torColor = lipgloss.Color("#34D399") // green
	case "bootstrapping":
		torColor = lipgloss.Color("#FBBF24") // amber
	default:
		torColor = lipgloss.Color("#F87171") // red
	}

	tor := torLabelStyle.Foreground(torColor).Render(fmt.Sprintf("tor:%s", s.TorState))
	peers := fmt.Sprintf("peers:%d", s.PeerCount)
	left := statusBarStyle.Render(fmt.Sprintf(" %s  %s  %s", addr, tor, peers))

	rightText := "ensemble"
	right := statusBarStyle.Render(rightText)

	gap := max(s.Width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	fill := statusBarStyle.Render(fmt.Sprintf("%*s", gap, ""))

	return left + fill + right
}
