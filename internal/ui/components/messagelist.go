package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	msgTimestamp = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	msgSenderMe = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7C3AED")).
			Bold(true)

	msgSenderThem = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Bold(true)

	msgAcked = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#34D399"))

	msgPending = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FBBF24"))
)

// ChatMessage holds data for rendering a message in the list.
type ChatMessage struct {
	ID        string
	Text      string
	SentByMe  bool
	Timestamp time.Time
	Acked     bool
}

// RenderMessages formats a slice of messages for display in a viewport.
func RenderMessages(messages []ChatMessage, width int) string {
	if len(messages) == 0 {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Italic(true).
			Render("No messages yet. Type a message below.")
	}

	var b strings.Builder
	for i, msg := range messages {
		ts := msgTimestamp.Render(msg.Timestamp.Format("15:04"))

		var sender string
		if msg.SentByMe {
			sender = msgSenderMe.Render("You")
		} else {
			sender = msgSenderThem.Render("Them")
		}

		ack := ""
		if msg.SentByMe {
			if msg.Acked {
				ack = " " + msgAcked.Render("✓")
			} else {
				ack = " " + msgPending.Render("…")
			}
		}

		fmt.Fprintf(&b, "%s %s: %s%s", ts, sender, msg.Text, ack)
		if i < len(messages)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
