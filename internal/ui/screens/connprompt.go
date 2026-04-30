package screens

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	promptTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FBBF24")). // amber for warning
			MarginBottom(1)

	promptAddr = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Bold(true)

	promptWarn = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Italic(true)

	promptKey = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Bold(true)

	promptKeyDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// ConnAcceptedMsg is emitted when the user accepts a connection.
type ConnAcceptedMsg struct {
	Address string
}

// ConnRejectedMsg is emitted when the user rejects a connection.
type ConnRejectedMsg struct {
	Address string
}

// ConnPrompt shows an incoming connection request.
type ConnPrompt struct {
	Address string
	Width   int
	Height  int
}

// NewConnPrompt creates a connection prompt for the given peer address.
func NewConnPrompt(addr string) ConnPrompt {
	return ConnPrompt{Address: addr}
}

// Init returns no initial command.
func (c ConnPrompt) Init() tea.Cmd {
	return nil
}

// Update handles key presses.
func (c ConnPrompt) Update(msg tea.Msg) (ConnPrompt, tea.Cmd) {
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		switch kmsg.String() {
		case "y":
			addr := c.Address
			return c, func() tea.Msg {
				return ConnAcceptedMsg{Address: addr}
			}
		case "n":
			addr := c.Address
			return c, func() tea.Msg {
				return ConnRejectedMsg{Address: addr}
			}
		}
	}
	return c, nil
}

// View renders the connection prompt.
func (c ConnPrompt) View() string {
	var b strings.Builder

	b.WriteString(promptTitle.Render("Incoming Connection Request") + "\n\n")

	short := c.Address
	if len(short) > 16 {
		short = short[:10] + "..." + short[len(short)-4:]
	}
	b.WriteString(fmt.Sprintf("Unknown peer %s wants to connect.\n\n", promptAddr.Render(short)))
	b.WriteString(promptWarn.Render("Accepting will reveal your public key to this peer.") + "\n\n")

	b.WriteString(fmt.Sprintf("%s %s  %s %s",
		promptKey.Render("y"),
		promptKeyDesc.Render("accept"),
		promptKey.Render("n"),
		promptKeyDesc.Render("reject"),
	))

	return b.String()
}
