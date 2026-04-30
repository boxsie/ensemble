package screens

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/boxsie/ensemble/internal/ui/components"
)

var (
	chatTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED"))

	chatDivider = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	chatKey = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	chatKeyDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// ChatSendMsg is emitted when the user sends a message.
type ChatSendMsg struct {
	Address string
	Text    string
}

// Chat is the conversation screen with a specific peer.
type Chat struct {
	Address    string // peer ensemble address
	Alias      string // peer display name
	ConnMethod string // "Direct" / "Relay" / "Tor"
	Messages   []components.ChatMessage
	input      textarea.Model
	viewport   viewport.Model
	Width      int
	Height     int
	ready      bool
}

// NewChat creates a chat screen for the given peer.
func NewChat(address, alias string) Chat {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.CharLimit = 4096
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()

	return Chat{
		Address: address,
		Alias:   alias,
		input:   ta,
	}
}

// Init starts the textarea blink.
func (c Chat) Init() tea.Cmd {
	return textarea.Blink
}

// Update handles messages.
func (c Chat) Update(msg tea.Msg) (Chat, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.Width = msg.Width
		c.Height = msg.Height
		c.initViewport()

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(c.input.Value())
			if text != "" {
				c.input.Reset()
				addr := c.Address
				return c, func() tea.Msg {
					return ChatSendMsg{Address: addr, Text: text}
				}
			}
			return c, nil
		}
	}

	// Update textarea.
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	cmds = append(cmds, cmd)

	// Update viewport.
	c.viewport, cmd = c.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return c, tea.Batch(cmds...)
}

// AddMessage adds a message to the chat and scrolls to the bottom.
func (c *Chat) AddMessage(msg components.ChatMessage) {
	c.Messages = append(c.Messages, msg)
	c.RefreshViewport()
}

// View renders the chat screen.
func (c Chat) View() string {
	if c.Width == 0 {
		return ""
	}

	var b strings.Builder

	// Header: peer name and connection method.
	name := c.Alias
	if name == "" {
		name = shortAddr(c.Address)
	}
	header := chatTitle.Render("Chat with "+name) + " "
	if c.ConnMethod != "" {
		header += chatKeyDesc.Render("["+c.ConnMethod+"]")
	}
	b.WriteString(header + "\n")
	b.WriteString(chatDivider.Render(strings.Repeat("─", min(c.Width, 60))) + "\n")

	// Message viewport.
	b.WriteString(c.viewport.View() + "\n")

	// Divider.
	b.WriteString(chatDivider.Render(strings.Repeat("─", min(c.Width, 60))) + "\n")

	// Input area.
	b.WriteString(c.input.View() + "\n")

	// Key hints.
	b.WriteString(fmt.Sprintf("%s %s  %s %s",
		chatKey.Render("enter"), chatKeyDesc.Render("send"),
		chatKey.Render("esc"), chatKeyDesc.Render("back"),
	))

	return b.String()
}

// initViewport creates the viewport with the right dimensions.
func (c *Chat) initViewport() {
	// Height: total - header(2) - divider(1) - input(3) - divider(1) - hints(1) = total - 8
	vpHeight := c.Height - 8
	if vpHeight < 1 {
		vpHeight = 1
	}
	c.viewport = viewport.New(c.Width, vpHeight)
	c.viewport.SetContent(components.RenderMessages(c.Messages, c.Width))
	c.viewport.GotoBottom()
	c.input.SetWidth(c.Width)
	c.ready = true
}

// RefreshViewport re-renders messages and scrolls to bottom.
func (c *Chat) RefreshViewport() {
	if !c.ready {
		return
	}
	c.viewport.SetContent(components.RenderMessages(c.Messages, c.Width))
	c.viewport.GotoBottom()
}

func shortAddr(addr string) string {
	if len(addr) > 16 {
		return addr[:10] + "..." + addr[len(addr)-4:]
	}
	return addr
}
