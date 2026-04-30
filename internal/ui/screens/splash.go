package screens

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var splashTitle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#7C3AED"))

var splashSubtle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#6B7280"))

var splashProgress = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#FBBF24"))

// TorProgressMsg updates the splash screen with Tor bootstrap progress.
type TorProgressMsg struct {
	Percent int
	Summary string
}

// TorReadyMsg signals that Tor has finished bootstrapping.
type TorReadyMsg struct {
	OnionAddr string
}

// Splash is the startup splash screen.
type Splash struct {
	Width    int
	Height   int
	Progress int
	Summary  string
}

// NewSplash creates a new splash screen.
func NewSplash() Splash {
	return Splash{}
}

// Init returns no initial command.
func (s Splash) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (s Splash) Update(msg tea.Msg) (Splash, tea.Cmd) {
	switch msg := msg.(type) {
	case TorProgressMsg:
		s.Progress = msg.Percent
		s.Summary = msg.Summary
	}
	return s, nil
}

// View renders the splash screen.
func (s Splash) View() string {
	logo := splashTitle.Render("ensemble")
	subtitle := splashSubtle.Render("decentralized messaging")

	var status string
	if s.Progress > 0 && s.Progress < 100 {
		bar := progressBar(s.Progress, 30)
		status = splashProgress.Render(fmt.Sprintf("Starting Tor... %d%%", s.Progress)) +
			"\n" + splashSubtle.Render(bar) +
			"\n" + splashSubtle.Render(s.Summary)
	} else {
		status = splashSubtle.Render("Starting...")
	}

	content := fmt.Sprintf("\n\n\n%s\n%s\n\n%s", logo, subtitle, status)

	return lipgloss.Place(s.Width, s.Height,
		lipgloss.Center, lipgloss.Center,
		content,
	)
}

// progressBar renders a simple text progress bar.
func progressBar(pct, width int) string {
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	bar := make([]byte, width)
	for i := range bar {
		if i < filled {
			bar[i] = '#'
		} else {
			bar[i] = '-'
		}
	}
	return "[" + string(bar) + "]"
}
