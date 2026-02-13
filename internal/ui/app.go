package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/ensemble/internal/chat"
	"github.com/boxsie/ensemble/internal/filetransfer"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/transport"
	"github.com/boxsie/ensemble/internal/ui/components"
	"github.com/boxsie/ensemble/internal/ui/screens"
)

// Screen identifies the current TUI screen.
type Screen int

const (
	ScreenSplash Screen = iota
	ScreenHome
	ScreenSettings
	ScreenAddContact
	ScreenConnPrompt
	ScreenChat
	ScreenFilePicker
	ScreenFileOffer
	ScreenTransfer
	ScreenAddNode
	ScreenDebug
)

// identityLoadedMsg is sent when identity has been fetched from the backend.
type identityLoadedMsg struct {
	Address   string
	PublicKey []byte
}

// statusLoadedMsg is sent when status has been fetched from the backend.
type statusLoadedMsg struct {
	TorState  string
	PeerCount int32
	OnionAddr string
	RTSize    int32
}

// debugLoadedMsg is sent when debug info has been fetched from the backend.
type debugLoadedMsg struct {
	Info *DebugInfo
}

// contactsLoadedMsg is sent when contacts have been fetched from the backend.
type contactsLoadedMsg struct {
	Contacts []screens.ContactItem
}

// contactSavedMsg signals a contact was saved successfully.
type contactSavedMsg struct{}

// contactSaveErrMsg signals a contact save failed.
type contactSaveErrMsg struct{ err error }

// nodeSavedMsg signals a bootstrap node was added successfully.
type nodeSavedMsg struct{ peersFound int }

// nodeSaveErrMsg signals a bootstrap node add failed.
type nodeSaveErrMsg struct{ err error }

// nodeScreenDoneMsg signals the add-node success screen should close.
type nodeScreenDoneMsg struct{}

// connDecisionDoneMsg signals that accept/reject completed.
type connDecisionDoneMsg struct{ err error }

// chatSentMsg is returned after a chat message send attempt.
type chatSentMsg struct {
	messageID string
	err       error
}

// fileSendDoneMsg is returned after a file send completes or fails.
type fileSendDoneMsg struct {
	transferID string
	err        error
}

// fileDecisionDoneMsg signals that accept/reject completed.
type fileDecisionDoneMsg struct{ err error }

// eventMsg wraps a node event received from the backend subscription.
type eventMsg node.Event

// App is the root Bubble Tea model.
type App struct {
	backend       Backend
	keys          KeyMap
	screen        Screen
	prevScreen    Screen // screen to return to after conn prompt
	splash        screens.Splash
	home          screens.Home
	settings      screens.Settings
	addContact    screens.AddContact
	addNode       screens.AddNode
	connPrompt    screens.ConnPrompt
	chat          screens.Chat
	filePicker    screens.FilePicker
	fileOffer     screens.FileOfferPrompt
	transfer      screens.Transfer
	debug         screens.Debug
	status        components.StatusBar
	width         int
	height        int
	identityReady bool
	torReady      bool
	eventChan     <-chan node.Event
}

// NewApp creates the root TUI model.
func NewApp(backend Backend) App {
	return App{
		backend:    backend,
		keys:       DefaultKeyMap(),
		screen:     ScreenSplash,
		splash:     screens.NewSplash(),
		home:       screens.NewHome(),
		settings:   screens.NewSettings(),
		addContact: screens.NewAddContact(),
		addNode:    screens.NewAddNode(),
		status:     components.NewStatusBar(),
	}
}

// Init starts async identity loading and event subscription.
func (a App) Init() tea.Cmd {
	return tea.Batch(
		a.loadIdentity,
		a.loadStatus,
		a.subscribeEvents,
	)
}

func (a App) loadIdentity() tea.Msg {
	info, err := a.backend.GetIdentity(context.Background())
	if err != nil {
		return identityLoadedMsg{}
	}
	return identityLoadedMsg{
		Address:   info.Address,
		PublicKey: info.PublicKey,
	}
}

func (a App) loadStatus() tea.Msg {
	info, err := a.backend.GetStatus(context.Background())
	if err != nil {
		return statusLoadedMsg{}
	}
	return statusLoadedMsg{
		TorState:  info.TorState,
		PeerCount: info.PeerCount,
		OnionAddr: info.OnionAddr,
		RTSize:    info.RTSize,
	}
}

func (a App) loadDebugInfo() tea.Msg {
	info, err := a.backend.GetDebugInfo(context.Background())
	if err != nil {
		return debugLoadedMsg{}
	}
	return debugLoadedMsg{Info: info}
}

func (a App) loadContacts() tea.Msg {
	list, err := a.backend.ListContacts(context.Background())
	if err != nil {
		return contactsLoadedMsg{}
	}
	var items []screens.ContactItem
	for _, c := range list {
		items = append(items, screens.ContactItem{
			Address: c.Address,
			Alias:   c.Alias,
		})
	}
	return contactsLoadedMsg{Contacts: items}
}

// eventChanMsg carries the event channel so we can chain reads.
type eventChanMsg <-chan node.Event

// subscribeEvents starts listening for daemon events.
func (a App) subscribeEvents() tea.Msg {
	ch, err := a.backend.Subscribe(context.Background())
	if err != nil {
		return nil
	}
	return eventChanMsg(ch)
}

// waitForEvent returns a tea.Cmd that reads the next event from the channel.
func waitForEvent(ch <-chan node.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(evt)
	}
}

// Update handles all messages.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.status.Width = msg.Width
		contentHeight := msg.Height - 1 // reserve 1 line for status bar
		a.splash.Width = msg.Width
		a.splash.Height = contentHeight
		a.home.Width = msg.Width
		a.home.Height = contentHeight
		a.settings.Width = msg.Width
		a.settings.Height = contentHeight
		a.addContact.Width = msg.Width
		a.addContact.Height = contentHeight
		a.addNode.Width = msg.Width
		a.addNode.Height = contentHeight
		a.connPrompt.Width = msg.Width
		a.connPrompt.Height = contentHeight
		if a.screen == ScreenChat {
			a.chat.Width = msg.Width
			a.chat.Height = contentHeight
		}

	case identityLoadedMsg:
		a.status.Address = msg.Address
		a.settings.Address = msg.Address
		a.settings.PublicKey = msg.PublicKey
		a.identityReady = true
		if a.maybeTransition() {
			return a, a.loadContacts
		}

	case statusLoadedMsg:
		a.status.TorState = msg.TorState
		a.status.PeerCount = msg.PeerCount
		a.status.RTSize = msg.RTSize
		if msg.TorState == "ready" || msg.TorState == "disabled" {
			a.torReady = true
			if a.maybeTransition() {
				return a, a.loadContacts
			}
		}

	case contactsLoadedMsg:
		a.home.Contacts = msg.Contacts
		return a, nil

	case debugLoadedMsg:
		if msg.Info != nil {
			a.debug.RTSize = msg.Info.RTSize
			a.debug.OnionAddr = msg.Info.OnionAddr
			a.debug.RTPeers = nil
			for _, p := range msg.Info.RTPeers {
				a.debug.RTPeers = append(a.debug.RTPeers, screens.DebugPeer{
					Address:   p.Address,
					OnionAddr: p.OnionAddr,
					LastSeen:  p.LastSeen,
				})
			}
			a.debug.Connections = nil
			for _, c := range msg.Info.Connections {
				a.debug.Connections = append(a.debug.Connections, screens.DebugConn{
					Address: c.Address,
					State:   c.State,
					Error:   c.Error,
				})
			}
		}
		return a, nil

	case contactSavedMsg:
		a.screen = ScreenHome
		return a, a.loadContacts

	case contactSaveErrMsg:
		return a, nil

	case connDecisionDoneMsg:
		a.screen = a.prevScreen
		return a, nil

	case screens.AddContactSavedMsg:
		return a, a.saveContact(msg.Address, msg.Alias)

	case screens.AddNodeSavedMsg:
		a.addNode.SetStatus("Connecting...")
		return a, a.saveNode(msg.OnionAddr)

	case nodeSavedMsg:
		if msg.peersFound > 0 {
			a.addNode.SetSuccess(fmt.Sprintf("Discovered %d peer(s). Returning home...", msg.peersFound))
		} else {
			a.addNode.SetSuccess("Connected but no peers found yet. Returning home...")
		}
		return a, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return nodeScreenDoneMsg{}
		})

	case nodeSaveErrMsg:
		a.addNode.SetError(msg.err.Error())
		return a, nil

	case nodeScreenDoneMsg:
		a.screen = ScreenHome
		return a, nil

	case screens.ConnAcceptedMsg:
		return a, a.acceptConn(msg.Address)

	case screens.ConnRejectedMsg:
		return a, a.rejectConn(msg.Address)

	case screens.ChatSendMsg:
		// Optimistically add the outgoing message to the chat.
		a.chat.AddMessage(components.ChatMessage{
			Text:      msg.Text,
			SentByMe:  true,
			Timestamp: time.Now(),
			Acked:     false,
		})
		return a, a.sendChatMessage(msg.Address, msg.Text)

	case chatSentMsg:
		if msg.err == nil {
			// Mark the last pending outgoing message as acked.
			for i := len(a.chat.Messages) - 1; i >= 0; i-- {
				if a.chat.Messages[i].SentByMe && !a.chat.Messages[i].Acked {
					a.chat.Messages[i].Acked = true
					a.chat.Messages[i].ID = msg.messageID
					break
				}
			}
			a.chat.RefreshViewport()
		}
		return a, nil

	case screens.FileSelectedMsg:
		// User selected a file to send — start the transfer and show transfers screen.
		a.transfer.Items = append(a.transfer.Items, screens.TransferItem{
			Filename:  filepath.Base(msg.Path),
			Direction: "send",
			PeerAddr:  msg.Address,
		})
		a.screen = ScreenTransfer
		return a, a.sendFile(msg.Address, msg.Path)

	case fileSendDoneMsg:
		// Update transfer item
		for i := range a.transfer.Items {
			if msg.err != nil {
				if a.transfer.Items[i].Direction == "send" && !a.transfer.Items[i].Complete && a.transfer.Items[i].Error == "" {
					a.transfer.Items[i].Error = msg.err.Error()
					break
				}
			} else {
				if a.transfer.Items[i].Direction == "send" && !a.transfer.Items[i].Complete {
					a.transfer.Items[i].TransferID = msg.transferID
					a.transfer.Items[i].Complete = true
					a.transfer.Items[i].Percent = 100
					break
				}
			}
		}
		return a, nil

	case screens.FileAcceptedMsg:
		return a, a.acceptFile(msg.TransferID)

	case screens.FileRejectedMsg:
		return a, a.rejectFile(msg.TransferID)

	case fileDecisionDoneMsg:
		a.screen = a.prevScreen
		return a, nil

	case screens.TransferCancelMsg:
		// TODO: wire cancel through backend
		return a, nil

	case eventChanMsg:
		a.eventChan = (<-chan node.Event)(msg)
		return a, waitForEvent(a.eventChan)

	case eventMsg:
		return a.handleEvent(node.Event(msg))

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Delegate to active screen.
	var cmd tea.Cmd
	switch a.screen {
	case ScreenSplash:
		a.splash, cmd = a.splash.Update(msg)
	case ScreenHome:
		a.home, cmd = a.home.Update(msg)
	case ScreenSettings:
		a.settings, cmd = a.settings.Update(msg)
	case ScreenAddContact:
		a.addContact, cmd = a.addContact.Update(msg)
	case ScreenAddNode:
		a.addNode, cmd = a.addNode.Update(msg)
	case ScreenConnPrompt:
		a.connPrompt, cmd = a.connPrompt.Update(msg)
	case ScreenChat:
		a.chat, cmd = a.chat.Update(msg)
	}
	return a, cmd
}

func (a App) saveContact(addr, alias string) tea.Cmd {
	return func() tea.Msg {
		if err := a.backend.AddContact(context.Background(), addr, alias, nil); err != nil {
			return contactSaveErrMsg{err: err}
		}
		return contactSavedMsg{}
	}
}

func (a App) saveNode(onionAddr string) tea.Cmd {
	return func() tea.Msg {
		peersFound, err := a.backend.AddNode(context.Background(), onionAddr)
		if err != nil {
			return nodeSaveErrMsg{err: err}
		}
		return nodeSavedMsg{peersFound: peersFound}
	}
}

func (a App) acceptConn(addr string) tea.Cmd {
	return func() tea.Msg {
		err := a.backend.AcceptConnection(context.Background(), addr)
		return connDecisionDoneMsg{err: err}
	}
}

func (a App) rejectConn(addr string) tea.Cmd {
	return func() tea.Msg {
		err := a.backend.RejectConnection(context.Background(), addr)
		return connDecisionDoneMsg{err: err}
	}
}

func (a App) sendChatMessage(addr, text string) tea.Cmd {
	return func() tea.Msg {
		msgID, err := a.backend.SendMessage(context.Background(), addr, text)
		return chatSentMsg{messageID: msgID, err: err}
	}
}

func (a App) sendFile(addr, path string) tea.Cmd {
	return func() tea.Msg {
		ch, err := a.backend.SendFile(context.Background(), addr, path)
		if err != nil {
			return fileSendDoneMsg{err: err}
		}
		// Wait for the transfer to complete.
		for p := range ch {
			if p.Error != "" {
				return fileSendDoneMsg{err: fmt.Errorf("%s", p.Error)}
			}
			if p.Complete {
				return fileSendDoneMsg{transferID: p.TransferID}
			}
		}
		return fileSendDoneMsg{err: fmt.Errorf("transfer stream closed")}
	}
}

func (a App) acceptFile(transferID string) tea.Cmd {
	return func() tea.Msg {
		err := a.backend.AcceptFile(context.Background(), transferID, "")
		return fileDecisionDoneMsg{err: err}
	}
}

func (a App) rejectFile(transferID string) tea.Cmd {
	return func() tea.Msg {
		err := a.backend.RejectFile(context.Background(), transferID)
		return fileDecisionDoneMsg{err: err}
	}
}

// handleEvent processes node events and chains the next read.
func (a App) handleEvent(evt node.Event) (tea.Model, tea.Cmd) {
	next := waitForEvent(a.eventChan) // always chain the next event read

	switch evt.Type {
	case node.EventTorBootstrap:
		if be, ok := evt.Payload.(map[string]any); ok {
			pct, _ := be["percent"].(float64)
			summary, _ := be["summary"].(string)
			a.splash.Progress = int(pct)
			a.splash.Summary = summary
			a.status.TorState = "bootstrapping"
		}

	case node.EventTorReady:
		a.status.TorState = "ready"
		if addr, ok := evt.Payload.(string); ok && addr != "" {
			a.status.OnionAddr = addr
		}
		a.torReady = true
		if a.maybeTransition() {
			return a, tea.Batch(next, a.loadStatus, a.loadContacts)
		}
		return a, tea.Batch(next, a.loadStatus)

	case node.EventConnectionReq:
		if addr, ok := evt.Payload.(string); ok {
			a.prevScreen = a.screen
			a.screen = ScreenConnPrompt
			a.connPrompt = screens.NewConnPrompt(addr)
			a.connPrompt.Width = a.width
			a.connPrompt.Height = a.height - 1
		}

	case node.EventChatMessage:
		if msg, ok := evt.Payload.(*chat.Message); ok {
			// If chat is open with this peer, add the incoming message.
			if a.screen == ScreenChat && a.chat.Address == msg.From {
				a.chat.AddMessage(components.ChatMessage{
					ID:        msg.ID,
					Text:      msg.Text,
					SentByMe:  false,
					Timestamp: msg.SentAt,
					Acked:     true,
				})
			}
		}

	case node.EventFileOffer:
		if offer, ok := evt.Payload.(*filetransfer.FileOffer); ok {
			a.prevScreen = a.screen
			a.screen = ScreenFileOffer
			a.fileOffer = screens.NewFileOfferPrompt(offer.TransferID, offer.Filename, offer.Size, offer.FromAddr)
			a.fileOffer.Width = a.width
			a.fileOffer.Height = a.height - 1
		}

	case node.EventConnectionState:
		if se, ok := evt.Payload.(transport.StateEvent); ok {
			stateStr := se.State.String()
			for i := range a.home.Contacts {
				if a.home.Contacts[i].Address == se.PeerAddr {
					a.home.Contacts[i].Status = stateStr
					break
				}
			}
		}

	case node.EventPeerOnline:
		if status, ok := evt.Payload.(transport.OnlineStatus); ok {
			for i := range a.home.Contacts {
				if a.home.Contacts[i].Address == status.Address {
					a.home.Contacts[i].Online = status.Online
					break
				}
			}
		}
	}
	return a, next
}

// maybeTransition moves from splash to home when both identity and tor are ready.
// Returns true if a transition occurred.
func (a *App) maybeTransition() bool {
	if a.identityReady && a.torReady && a.screen == ScreenSplash {
		a.screen = ScreenHome
		return true
	}
	return false
}

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global quit — but not when typing in a text field.
	if a.screen != ScreenAddContact && a.screen != ScreenAddNode && a.screen != ScreenConnPrompt && a.screen != ScreenChat && a.screen != ScreenFileOffer {
		switch {
		case msg.String() == "q" || msg.String() == "ctrl+c":
			return a, tea.Quit
		case msg.String() == "esc":
			if a.screen != ScreenHome && a.screen != ScreenSplash {
				a.screen = ScreenHome
			}
			return a, nil
		case msg.String() == "a":
			if a.screen == ScreenHome {
				a.addContact.Reset()
				a.screen = ScreenAddContact
				return a, a.addContact.Init()
			}
		case msg.String() == "n":
			if a.screen == ScreenHome {
				a.addNode.Reset()
				a.screen = ScreenAddNode
				return a, a.addNode.Init()
			}
		case msg.String() == "d":
			if a.screen == ScreenHome {
				a.debug = screens.NewDebug()
				a.debug.Width = a.width
				a.debug.Height = a.height - 1
				a.screen = ScreenDebug
				return a, a.loadDebugInfo
			}
		case msg.String() == "s":
			if a.screen == ScreenHome {
				a.screen = ScreenSettings
				return a, nil
			}
		case msg.String() == "f":
			if a.screen == ScreenHome && len(a.home.Contacts) > 0 {
				c := a.home.Contacts[a.home.Cursor]
				a.filePicker = screens.NewFilePicker(c.Address, c.Alias)
				a.filePicker.Width = a.width
				a.filePicker.Height = a.height - 1
				a.screen = ScreenFilePicker
				return a, nil
			}
		case msg.String() == "t":
			if a.screen == ScreenHome {
				a.screen = ScreenTransfer
				return a, nil
			}
		case msg.String() == "enter":
			if a.screen == ScreenHome && len(a.home.Contacts) > 0 {
				c := a.home.Contacts[a.home.Cursor]
				a.chat = screens.NewChat(c.Address, c.Alias)
				a.chat.Width = a.width
				a.chat.Height = a.height - 1
				a.screen = ScreenChat
				return a, a.chat.Init()
			}
		}
	} else if a.screen == ScreenChat {
		// Esc from chat returns home.
		if msg.String() == "esc" {
			a.screen = ScreenHome
			return a, nil
		}
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	} else if a.screen == ScreenAddContact || a.screen == ScreenAddNode {
		// Esc from add contact/node returns home.
		if msg.String() == "esc" {
			a.screen = ScreenHome
			return a, nil
		}
		// ctrl+c always quits.
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	} else if a.screen == ScreenConnPrompt || a.screen == ScreenFileOffer {
		// ctrl+c always quits.
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	}

	// Delegate key to current screen.
	var cmd tea.Cmd
	switch a.screen {
	case ScreenHome:
		a.home, cmd = a.home.Update(msg)
	case ScreenSettings:
		a.settings, cmd = a.settings.Update(msg)
	case ScreenAddContact:
		a.addContact, cmd = a.addContact.Update(msg)
	case ScreenAddNode:
		a.addNode, cmd = a.addNode.Update(msg)
	case ScreenConnPrompt:
		a.connPrompt, cmd = a.connPrompt.Update(msg)
	case ScreenChat:
		a.chat, cmd = a.chat.Update(msg)
	case ScreenFilePicker:
		a.filePicker, cmd = a.filePicker.Update(msg)
	case ScreenFileOffer:
		a.fileOffer, cmd = a.fileOffer.Update(msg)
	case ScreenTransfer:
		a.transfer, cmd = a.transfer.Update(msg)
	}
	return a, cmd
}

// View renders the current screen plus the status bar.
func (a App) View() string {
	var content string
	switch a.screen {
	case ScreenSplash:
		content = a.splash.View()
	case ScreenHome:
		content = a.home.View()
	case ScreenSettings:
		content = a.settings.View()
	case ScreenAddContact:
		content = a.addContact.View()
	case ScreenAddNode:
		content = a.addNode.View()
	case ScreenConnPrompt:
		content = a.connPrompt.View()
	case ScreenChat:
		content = a.chat.View()
	case ScreenFilePicker:
		content = a.filePicker.View()
	case ScreenFileOffer:
		content = a.fileOffer.View()
	case ScreenTransfer:
		content = a.transfer.View()
	case ScreenDebug:
		content = a.debug.View()
	}

	return content + "\n" + a.status.View()
}
