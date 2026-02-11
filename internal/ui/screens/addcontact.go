package screens

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	addContactTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			MarginBottom(1)

	addContactLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Bold(true)

	addContactHint = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	addContactErr = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F87171"))
)

// AddContactSavedMsg is emitted when a contact is successfully saved.
type AddContactSavedMsg struct {
	Address string
	Alias   string
}

// AddContactField tracks which field is focused.
type AddContactField int

const (
	FieldAddress AddContactField = iota
	FieldAlias
)

// AddContact is the add-contact screen.
type AddContact struct {
	addressInput textinput.Model
	aliasInput   textinput.Model
	focused      AddContactField
	err          string
	Width        int
	Height       int
}

// NewAddContact creates a new add-contact screen.
func NewAddContact() AddContact {
	addr := textinput.New()
	addr.Placeholder = "E..."
	addr.CharLimit = 64
	addr.Focus()

	alias := textinput.New()
	alias.Placeholder = "(optional)"
	alias.CharLimit = 32

	return AddContact{
		addressInput: addr,
		aliasInput:   alias,
		focused:      FieldAddress,
	}
}

// Init returns no initial command.
func (a AddContact) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages.
func (a AddContact) Update(msg tea.Msg) (AddContact, tea.Cmd) {
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		switch kmsg.String() {
		case "tab", "shift+tab":
			a.err = ""
			if a.focused == FieldAddress {
				a.focused = FieldAlias
				a.addressInput.Blur()
				a.aliasInput.Focus()
			} else {
				a.focused = FieldAddress
				a.aliasInput.Blur()
				a.addressInput.Focus()
			}
			return a, nil

		case "enter":
			addr := strings.TrimSpace(a.addressInput.Value())
			if addr == "" {
				a.err = "Address is required"
				return a, nil
			}
			if addr[0] != 'E' {
				a.err = "Address must start with 'E'"
				return a, nil
			}
			alias := strings.TrimSpace(a.aliasInput.Value())
			return a, func() tea.Msg {
				return AddContactSavedMsg{
					Address: addr,
					Alias:   alias,
				}
			}
		}
	}

	var cmd tea.Cmd
	if a.focused == FieldAddress {
		a.addressInput, cmd = a.addressInput.Update(msg)
	} else {
		a.aliasInput, cmd = a.aliasInput.Update(msg)
	}
	return a, cmd
}

// View renders the add-contact screen.
func (a AddContact) View() string {
	var b strings.Builder
	b.WriteString(addContactTitle.Render("Add Contact") + "\n\n")

	b.WriteString(addContactLabel.Render("Address") + "\n")
	b.WriteString(a.addressInput.View() + "\n\n")

	b.WriteString(addContactLabel.Render("Alias") + "\n")
	b.WriteString(a.aliasInput.View() + "\n\n")

	if a.err != "" {
		b.WriteString(addContactErr.Render(a.err) + "\n\n")
	}

	b.WriteString(fmt.Sprintf("%s %s  %s %s  %s %s",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true).Render("enter"),
		addContactHint.Render("save"),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true).Render("tab"),
		addContactHint.Render("next field"),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true).Render("esc"),
		addContactHint.Render("cancel"),
	))

	return b.String()
}

// Reset clears the form for reuse.
func (a *AddContact) Reset() {
	a.addressInput.SetValue("")
	a.aliasInput.SetValue("")
	a.addressInput.Focus()
	a.aliasInput.Blur()
	a.focused = FieldAddress
	a.err = ""
}
