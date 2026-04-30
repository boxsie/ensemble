package screens

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	foTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#F59E0B")).
		MarginBottom(1)

	foInfo = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#D1D5DB"))

	foKey = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	foKeyDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// FileAcceptedMsg is emitted when the user accepts a file offer.
type FileAcceptedMsg struct {
	TransferID string
}

// FileRejectedMsg is emitted when the user rejects a file offer.
type FileRejectedMsg struct {
	TransferID string
}

// FileOfferPrompt shows an incoming file offer and asks to accept or reject.
type FileOfferPrompt struct {
	TransferID string
	Filename   string
	Size       uint64
	FromAddr   string
	Width      int
	Height     int
}

// NewFileOfferPrompt creates a file offer prompt.
func NewFileOfferPrompt(transferID, filename string, size uint64, fromAddr string) FileOfferPrompt {
	return FileOfferPrompt{
		TransferID: transferID,
		Filename:   filename,
		Size:       size,
		FromAddr:   fromAddr,
	}
}

// Init returns no command.
func (f FileOfferPrompt) Init() tea.Cmd {
	return nil
}

// Update handles key events.
func (f FileOfferPrompt) Update(msg tea.Msg) (FileOfferPrompt, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y":
			id := f.TransferID
			return f, func() tea.Msg {
				return FileAcceptedMsg{TransferID: id}
			}
		case "n":
			id := f.TransferID
			return f, func() tea.Msg {
				return FileRejectedMsg{TransferID: id}
			}
		}
	}
	return f, nil
}

// View renders the file offer prompt.
func (f FileOfferPrompt) View() string {
	var b strings.Builder

	b.WriteString(foTitle.Render("Incoming File") + "\n\n")

	from := shortAddr(f.FromAddr)
	b.WriteString(foInfo.Render(fmt.Sprintf("%s is sending %s (%s)",
		from, f.Filename, formatSize(int64(f.Size)))) + "\n\n")

	b.WriteString(fmt.Sprintf("Accept? %s %s  %s %s",
		foKey.Render("y"), foKeyDesc.Render("accept"),
		foKey.Render("n"), foKeyDesc.Render("reject"),
	))

	return b.String()
}
