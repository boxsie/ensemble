package screens

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AddNodeSavedMsg is emitted when the user submits a seed node onion address.
type AddNodeSavedMsg struct {
	OnionAddr string
}

// AddNode is the add-node screen (single onion address field).
type AddNode struct {
	onionInput textinput.Model
	err        string
	Width      int
	Height     int
}

// NewAddNode creates a new add-node screen.
func NewAddNode() AddNode {
	input := textinput.New()
	input.Placeholder = "xxxxx.onion"
	input.CharLimit = 128
	input.Focus()

	return AddNode{
		onionInput: input,
	}
}

// Init returns the initial command (cursor blink).
func (a AddNode) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages.
func (a AddNode) Update(msg tea.Msg) (AddNode, tea.Cmd) {
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		switch kmsg.String() {
		case "enter":
			addr := strings.TrimSpace(a.onionInput.Value())
			if addr == "" {
				a.err = "Onion address is required"
				return a, nil
			}
			if !strings.HasSuffix(addr, ".onion") {
				a.err = "Address must end with .onion"
				return a, nil
			}
			return a, func() tea.Msg {
				return AddNodeSavedMsg{OnionAddr: addr}
			}
		}
	}

	var cmd tea.Cmd
	a.onionInput, cmd = a.onionInput.Update(msg)
	return a, cmd
}

// View renders the add-node screen.
func (a AddNode) View() string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7C3AED")).
		MarginBottom(1)

	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280"))

	errStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#F87171"))

	var b strings.Builder
	b.WriteString(title.Render("Add Bootstrap Node") + "\n\n")

	b.WriteString(label.Render("Onion Address") + "\n")
	b.WriteString(a.onionInput.View() + "\n\n")

	if a.err != "" {
		b.WriteString(errStyle.Render(a.err) + "\n\n")
	}

	b.WriteString(fmt.Sprintf("%s %s  %s %s",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true).Render("enter"),
		hint.Render("connect"),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true).Render("esc"),
		hint.Render("cancel"),
	))

	return b.String()
}

// Reset clears the form for reuse.
func (a *AddNode) Reset() {
	a.onionInput.SetValue("")
	a.onionInput.Focus()
	a.err = ""
}
