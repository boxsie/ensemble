package screens

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	homeTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			MarginBottom(1)

	homeEmpty = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Italic(true)

	homeKey = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	homeKeyDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	homeSelected = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7C3AED")).
			Bold(true)
)

// ContactItem represents a contact in the list.
type ContactItem struct {
	Address string
	Alias   string
	Online  bool
}

// Home is the main contact list screen.
type Home struct {
	Contacts []ContactItem
	Cursor   int
	Width    int
	Height   int
}

// NewHome creates a new home screen.
func NewHome() Home {
	return Home{}
}

// Init returns no initial command.
func (h Home) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (h Home) Update(msg tea.Msg) (Home, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "up", "k":
			if h.Cursor > 0 {
				h.Cursor--
			}
		case "down", "j":
			if h.Cursor < len(h.Contacts)-1 {
				h.Cursor++
			}
		}
	}
	return h, nil
}

// View renders the home screen.
func (h Home) View() string {
	var b strings.Builder
	b.WriteString(homeTitle.Render("Contacts") + "\n")

	if len(h.Contacts) == 0 {
		b.WriteString(homeEmpty.Render("No contacts yet. Press 'a' to add one.") + "\n")
	} else {
		for i, c := range h.Contacts {
			cursor := "  "
			name := c.Alias
			if name == "" {
				name = c.Address
			}
			if i == h.Cursor {
				cursor = "> "
				name = homeSelected.Render(name)
			}
			status := homeKeyDesc.Render("offline")
			if c.Online {
				status = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Render("online")
			}
			fmt.Fprintf(&b, "%s%s  %s\n", cursor, name, status)
		}
	}

	b.WriteString("\n")
	if len(h.Contacts) > 0 {
		fmt.Fprintf(&b, "%s %s  %s %s  ",
			homeKey.Render("enter"), homeKeyDesc.Render("chat"),
			homeKey.Render("f"), homeKeyDesc.Render("send file"),
		)
	}
	fmt.Fprintf(&b, "%s %s  %s %s  %s %s  %s %s  %s %s",
		homeKey.Render("a"), homeKeyDesc.Render("add contact"),
		homeKey.Render("n"), homeKeyDesc.Render("add node"),
		homeKey.Render("t"), homeKeyDesc.Render("transfers"),
		homeKey.Render("s"), homeKeyDesc.Render("settings"),
		homeKey.Render("q"), homeKeyDesc.Render("quit"),
	)

	return b.String()
}
