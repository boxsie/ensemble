package screens

import (
	"encoding/hex"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	settingsTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			MarginBottom(1)

	settingsLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Bold(true)

	settingsValue = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB"))

	settingsHint = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// Settings shows the node's identity information.
type Settings struct {
	Address   string
	PublicKey []byte
	Width     int
	Height    int
}

// NewSettings creates a new settings screen.
func NewSettings() Settings {
	return Settings{}
}

// Init returns no initial command.
func (s Settings) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (s Settings) Update(_ tea.Msg) (Settings, tea.Cmd) {
	return s, nil
}

// View renders the settings screen.
func (s Settings) View() string {
	out := settingsTitle.Render("Settings") + "\n\n"

	out += settingsLabel.Render("Address") + "\n"
	out += settingsValue.Render(s.Address) + "\n\n"

	out += settingsLabel.Render("Public Key") + "\n"
	pubHex := hex.EncodeToString(s.PublicKey)
	out += settingsValue.Render(pubHex) + "\n\n"

	out += fmt.Sprintf("%s %s",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true).Render("esc"),
		settingsHint.Render("back"),
	)

	return out
}
