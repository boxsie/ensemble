package screens

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	xferTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			MarginBottom(1)

	xferBar = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#34D399"))

	xferBarBg = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#374151"))

	xferInfo = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D1D5DB"))

	xferError = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444")).
			Bold(true)

	xferDone = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#34D399")).
			Bold(true)

	xferKey = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	xferKeyDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// TransferCancelMsg is emitted when the user cancels a transfer.
type TransferCancelMsg struct {
	TransferID string
}

// TransferItem represents an active file transfer.
type TransferItem struct {
	TransferID string
	Filename   string
	Direction  string // "send" or "receive"
	PeerAddr   string
	Percent    float32
	Speed      string // formatted speed string
	ETA        string // formatted ETA
	Complete   bool
	Error      string
}

// Transfer is the transfer monitoring screen.
type Transfer struct {
	Items  []TransferItem
	Cursor int
	Width  int
	Height int
}

// NewTransfer creates a new transfer screen.
func NewTransfer() Transfer {
	return Transfer{}
}

// Init returns no command.
func (t Transfer) Init() tea.Cmd {
	return nil
}

// Update handles key events.
func (t Transfer) Update(msg tea.Msg) (Transfer, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "up", "k":
			if t.Cursor > 0 {
				t.Cursor--
			}
		case "down", "j":
			if t.Cursor < len(t.Items)-1 {
				t.Cursor++
			}
		case "c":
			// Cancel selected transfer
			if len(t.Items) > 0 && t.Cursor < len(t.Items) {
				item := t.Items[t.Cursor]
				if !item.Complete && item.Error == "" {
					id := item.TransferID
					return t, func() tea.Msg {
						return TransferCancelMsg{TransferID: id}
					}
				}
			}
		}
	}
	return t, nil
}

// View renders the transfer screen.
func (t Transfer) View() string {
	var b strings.Builder

	b.WriteString(xferTitle.Render("File Transfers") + "\n")

	if len(t.Items) == 0 {
		b.WriteString(xferKeyDesc.Render("No active transfers.") + "\n")
		b.WriteString(fmt.Sprintf("\n%s %s",
			xferKey.Render("esc"), xferKeyDesc.Render("back"),
		))
		return b.String()
	}

	barWidth := t.Width - 30
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 40 {
		barWidth = 40
	}

	for i, item := range t.Items {
		cursor := "  "
		if i == t.Cursor {
			cursor = "> "
		}

		// Direction arrow
		arrow := "↑"
		if item.Direction == "receive" {
			arrow = "↓"
		}

		// Status line
		var statusLine string
		if item.Error != "" {
			statusLine = xferError.Render("Error: " + item.Error)
		} else if item.Complete {
			statusLine = xferDone.Render("Complete ✓")
		} else {
			// Progress bar
			filled := int(float32(barWidth) * item.Percent / 100)
			if filled > barWidth {
				filled = barWidth
			}
			bar := xferBar.Render(strings.Repeat("█", filled)) +
				xferBarBg.Render(strings.Repeat("░", barWidth-filled))
			pctStr := fmt.Sprintf(" %.0f%%", item.Percent)

			speedETA := ""
			if item.Speed != "" {
				speedETA = fmt.Sprintf("  %s", item.Speed)
			}
			if item.ETA != "" {
				speedETA += fmt.Sprintf("  ~%s", item.ETA)
			}

			statusLine = bar + pctStr + speedETA
		}

		fmt.Fprintf(&b, "%s%s %s  %s\n", cursor, arrow, xferInfo.Render(item.Filename), shortAddr(item.PeerAddr))
		fmt.Fprintf(&b, "   %s\n", statusLine)
	}

	b.WriteString(fmt.Sprintf("\n%s %s  %s %s",
		xferKey.Render("c"), xferKeyDesc.Render("cancel"),
		xferKey.Render("esc"), xferKeyDesc.Render("back"),
	))

	return b.String()
}
