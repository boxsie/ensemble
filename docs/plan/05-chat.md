# Phase 5: Chat — T-33 to T-37

## Goal
Send and receive encrypted text messages between peers.

---

## T-33: Chat service
Send and receive messages over libp2p streams.
- Register stream handler for `/ensemble/chat/1.0.0`
- `SendMessage(ctx, peerID, text) → Message` — open stream, write ChatText, wait for ACK
- Incoming messages → parse ChatText → emit ChatMessage event → send ACK
- Message struct: `{ ID, From, To, Text, SentAt, AckedAt, Direction }`
- **Files**: `internal/chat/service.go`, `internal/chat/service_test.go`
- **Verify**: Two connected hosts exchange messages. ACKs received. Events emitted.

## T-34: Chat history
In-memory per-conversation message store.
- `History.Append(msg)`, `GetConversation(peer)`, `GetLatest(peer, n)`, `Clear(peer)`
- Max 1000 messages per peer (FIFO eviction)
- **Files**: `internal/chat/history.go`, `internal/chat/history_test.go`
- **Verify**: Append messages, retrieve in order. Eviction at 1000. Clear works.

## T-35: Wire chat into node + gRPC
Expose chat via gRPC API.
- `SendMessage` RPC → chat service → peer, return ACK status
- Incoming messages → EventBus → gRPC Subscribe stream
- **Files**: Update `internal/node/node.go`, `internal/api/server.go`
- **Verify**: `grpcurl` calls `SendMessage`, message arrives at peer. Incoming messages appear on `Subscribe` stream.

## T-36: TUI chat screen
Conversation view with message input.
- Viewport: scrollable message history with timestamps
- Textarea: multi-line input, Enter to send
- Typing indicator (future: wire up ChatTyping)
- Show connection method in status bar (Direct / Relay / Tor)
- Message list component: format messages with sender, timestamp, ACK status
- **Files**: `internal/ui/screens/chat.go`, `internal/ui/components/messagelist.go`, update `internal/ui/components/statusbar.go`
- **Verify**: Open chat from home screen, type message, see it sent with ACK. Receive messages in real-time. Scroll history.

## T-37: TUI contact list online status
Show which contacts are online/offline.
- Presence check: ping connected peers every 30s
- Contact list shows green dot for online, gray for offline
- **Files**: Update `internal/ui/screens/home.go`, `internal/ui/components/contactlist.go`
- **Verify**: Connected peer shows online. Disconnected peer shows offline.
