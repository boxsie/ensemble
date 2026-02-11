package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/chat"
	"github.com/boxsie/ensemble/internal/filetransfer"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/transport"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setup(t *testing.T) (alice, bob *testPeer) {
	t.Helper()
	dialer := newTestDialer()
	finder := newMockFinder()
	alice = newTestPeer(t, "Alice", dialer, finder)
	bob = newTestPeer(t, "Bob", dialer, finder)
	return alice, bob
}

func setupConnected(t *testing.T) (alice, bob *testPeer) {
	t.Helper()
	alice, bob = setup(t)

	// Both sides add each other as contacts for auto-accept.
	alice.addContact(bob)
	bob.addContact(alice)

	// Connect in both directions so both connectors have peer entries.
	connectViaConnector(t, alice, bob)
	connectViaConnector(t, bob, alice)
	return alice, bob
}

func createTestFile(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile.bin")
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Connection tests (signaling handshake via Connector)
// ---------------------------------------------------------------------------

func TestSignalingHandshake_KnownContact(t *testing.T) {
	alice, bob := setup(t)

	// Bob adds Alice as a known contact — signaling will auto-accept.
	bob.addContact(alice)
	// Alice adds Bob so she can connect.
	alice.addContact(bob)

	connectViaConnector(t, alice, bob)

	// Verify connector state.
	pc := alice.connector.GetPeer(bob.addr)
	if pc == nil {
		t.Fatal("expected peer connection for Bob in Alice's connector")
	}
	if pc.State != transport.StateConnected && pc.State != transport.StateConnectedTor {
		t.Fatalf("expected connected state, got %s", pc.State)
	}
}

func TestSignalingHandshake_UnknownAccepted(t *testing.T) {
	alice, bob := setup(t)

	// Bob does NOT have Alice as contact, but auto-accepts via callback.
	bob.setOnConnectionRequest(func(fromAddr string) signaling.ConnectionDecision {
		return signaling.ConnectionDecision{Accepted: true}
	})

	connectViaConnector(t, alice, bob)

	pc := alice.connector.GetPeer(bob.addr)
	if pc == nil {
		t.Fatal("expected peer connection for Bob")
	}
}

func TestSignalingHandshake_UnknownRejected(t *testing.T) {
	alice, bob := setup(t)

	// Bob rejects unknown peers.
	bob.setOnConnectionRequest(func(fromAddr string) signaling.ConnectionDecision {
		return signaling.ConnectionDecision{Accepted: false, Reason: "go away"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := alice.connector.Connect(ctx, bob.addr)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if !errors.Is(err, signaling.ErrRejected) {
		t.Fatalf("expected ErrRejected, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Chat tests
// ---------------------------------------------------------------------------

func TestChat_SendAndReceive(t *testing.T) {
	alice, bob := setupConnected(t)

	ch := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, err := alice.chatSvc.SendMessage(ctx, bob.addr, "hello from integration")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.AckedAt == nil {
		t.Fatal("expected message to be ACKed")
	}
	if msg.Direction != chat.Outgoing {
		t.Fatalf("expected Outgoing, got %s", msg.Direction)
	}

	select {
	case e := <-ch:
		received := e.Payload.(*chat.Message)
		if received.Text != "hello from integration" {
			t.Fatalf("got %q, want %q", received.Text, "hello from integration")
		}
		if received.From != alice.addr {
			t.Fatalf("from: got %q, want %q", received.From, alice.addr)
		}
		if received.ID != msg.ID {
			t.Fatalf("ID mismatch: sent %q, received %q", msg.ID, received.ID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for chat event on Bob's bus")
	}
}

func TestChat_Bidirectional(t *testing.T) {
	alice, bob := setupConnected(t)

	aliceCh := alice.bus.Subscribe(node.EventChatMessage)
	bobCh := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Alice → Bob
	msg1, err := alice.chatSvc.SendMessage(ctx, bob.addr, "hi bob")
	if err != nil {
		t.Fatalf("Alice→Bob: %v", err)
	}
	select {
	case e := <-bobCh:
		if e.Payload.(*chat.Message).ID != msg1.ID {
			t.Fatal("Bob got wrong message")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Bob's event")
	}

	// Bob → Alice
	msg2, err := bob.chatSvc.SendMessage(ctx, alice.addr, "hi alice")
	if err != nil {
		t.Fatalf("Bob→Alice: %v", err)
	}
	select {
	case e := <-aliceCh:
		r := e.Payload.(*chat.Message)
		if r.ID != msg2.ID {
			t.Fatal("Alice got wrong message")
		}
		if r.Text != "hi alice" {
			t.Fatalf("got %q, want %q", r.Text, "hi alice")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Alice's event")
	}
}

func TestChat_MultipleMessages(t *testing.T) {
	alice, bob := setupConnected(t)

	bobCh := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	texts := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	ids := make([]string, len(texts))

	for i, text := range texts {
		msg, err := alice.chatSvc.SendMessage(ctx, bob.addr, text)
		if err != nil {
			t.Fatalf("SendMessage(%q): %v", text, err)
		}
		ids[i] = msg.ID
	}

	for i, text := range texts {
		select {
		case e := <-bobCh:
			r := e.Payload.(*chat.Message)
			if r.Text != text {
				t.Fatalf("message %d: got %q, want %q", i, r.Text, text)
			}
			if r.ID != ids[i] {
				t.Fatalf("message %d: ID mismatch", i)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for message %d", i)
		}
	}
}

func TestChat_History(t *testing.T) {
	alice, bob := setupConnected(t)

	bobCh := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgs := []string{"first", "second", "third"}
	for _, text := range msgs {
		if _, err := alice.chatSvc.SendMessage(ctx, bob.addr, text); err != nil {
			t.Fatalf("SendMessage(%q): %v", text, err)
		}
		// Drain Bob's event so it doesn't block.
		select {
		case <-bobCh:
		case <-ctx.Done():
			t.Fatal("timed out")
		}
	}

	// Alice's history should have outgoing messages.
	aliceConvo := alice.chatHist.GetConversation(bob.addr)
	if len(aliceConvo) != 3 {
		t.Fatalf("Alice history: got %d messages, want 3", len(aliceConvo))
	}
	for i, m := range aliceConvo {
		if m.Text != msgs[i] {
			t.Fatalf("Alice history[%d]: got %q, want %q", i, m.Text, msgs[i])
		}
		if m.Direction != chat.Outgoing {
			t.Fatalf("Alice history[%d]: expected Outgoing", i)
		}
	}

	// Bob's history should have incoming messages.
	bobConvo := bob.chatHist.GetConversation(alice.addr)
	if len(bobConvo) != 3 {
		t.Fatalf("Bob history: got %d messages, want 3", len(bobConvo))
	}
	for i, m := range bobConvo {
		if m.Text != msgs[i] {
			t.Fatalf("Bob history[%d]: got %q, want %q", i, m.Text, msgs[i])
		}
		if m.Direction != chat.Incoming {
			t.Fatalf("Bob history[%d]: expected Incoming", i)
		}
	}
}

// ---------------------------------------------------------------------------
// File transfer tests
// ---------------------------------------------------------------------------

func TestFileTransfer_SmallFile(t *testing.T) {
	alice, bob := setupConnected(t)

	srcPath := createTestFile(t, 3*1024) // 3KB

	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*filetransfer.FileOffer)
		offer.Decision <- filetransfer.FileDecision{Accept: true, SavePath: bob.ftSaveDir}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	xfer, err := alice.ftSender.OfferFile(ctx, bob.addr, srcPath)
	if err != nil {
		t.Fatalf("OfferFile: %v", err)
	}
	if xfer.Acked() != xfer.Total {
		t.Fatalf("not all chunks acked: %d/%d", xfer.Acked(), xfer.Total)
	}

	// Verify integrity.
	srcData, _ := os.ReadFile(srcPath)
	rcvData, err := os.ReadFile(filepath.Join(bob.ftSaveDir, "testfile.bin"))
	if err != nil {
		t.Fatalf("reading received file: %v", err)
	}
	if sha256.Sum256(srcData) != sha256.Sum256(rcvData) {
		t.Fatal("file hash mismatch")
	}
}

func TestFileTransfer_LargerFile(t *testing.T) {
	alice, bob := setupConnected(t)

	srcPath := createTestFile(t, 100*1024) // 100KB

	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*filetransfer.FileOffer)
		offer.Decision <- filetransfer.FileDecision{Accept: true, SavePath: bob.ftSaveDir}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	xfer, err := alice.ftSender.OfferFile(ctx, bob.addr, srcPath)
	if err != nil {
		t.Fatalf("OfferFile: %v", err)
	}
	if xfer.Acked() != xfer.Total {
		t.Fatalf("not all chunks acked: %d/%d", xfer.Acked(), xfer.Total)
	}

	srcData, _ := os.ReadFile(srcPath)
	rcvData, err := os.ReadFile(filepath.Join(bob.ftSaveDir, "testfile.bin"))
	if err != nil {
		t.Fatalf("reading received file: %v", err)
	}
	if sha256.Sum256(srcData) != sha256.Sum256(rcvData) {
		t.Fatal("file hash mismatch")
	}
}

func TestFileTransfer_Rejected(t *testing.T) {
	alice, bob := setupConnected(t)

	srcPath := createTestFile(t, 1024)

	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*filetransfer.FileOffer)
		offer.Decision <- filetransfer.FileDecision{Accept: false, Reason: "no thanks"}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := alice.ftSender.OfferFile(ctx, bob.addr, srcPath)
	if err == nil {
		t.Fatal("expected error for rejected file transfer")
	}
}

func TestFileTransfer_ProgressCallbacks(t *testing.T) {
	alice, bob := setupConnected(t)

	srcPath := createTestFile(t, 5*1024)

	var mu sync.Mutex
	var senderProgress []uint32

	alice.ftSender.SetProgressCallback(func(_ string, acked, total uint32) {
		mu.Lock()
		senderProgress = append(senderProgress, acked)
		mu.Unlock()
	})

	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*filetransfer.FileOffer)
		offer.Decision <- filetransfer.FileDecision{Accept: true, SavePath: bob.ftSaveDir}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	xfer, err := alice.ftSender.OfferFile(ctx, bob.addr, srcPath)
	if err != nil {
		t.Fatalf("OfferFile: %v", err)
	}

	mu.Lock()
	count := len(senderProgress)
	mu.Unlock()

	if count == 0 {
		t.Fatal("expected sender progress callbacks")
	}
	if uint32(count) != xfer.Total {
		t.Fatalf("expected %d progress updates, got %d", xfer.Total, count)
	}
}

// ---------------------------------------------------------------------------
// Presence tests
// ---------------------------------------------------------------------------

func TestPresence_PingPong(t *testing.T) {
	alice, bob := setupConnected(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pc := alice.connector.GetPeer(bob.addr)
	if pc == nil {
		t.Fatal("expected peer connection")
	}

	rtt, err := alice.presence.PingPeer(ctx, pc.PeerID)
	if err != nil {
		t.Fatalf("PingPeer: %v", err)
	}
	if rtt <= 0 {
		t.Fatalf("expected positive RTT, got %v", rtt)
	}
}

func TestPresence_Disconnect(t *testing.T) {
	alice, bob := setupConnected(t)

	pc := alice.connector.GetPeer(bob.addr)
	if pc == nil {
		t.Fatal("expected peer connection")
	}

	// Close Bob's host to simulate disconnect.
	bob.host.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := alice.presence.PingPeer(ctx, pc.PeerID)
	if err == nil {
		t.Fatal("expected ping to fail after disconnect")
	}
}

// ---------------------------------------------------------------------------
// Connection management tests
// ---------------------------------------------------------------------------

func TestDisconnectAndReconnect(t *testing.T) {
	alice, bob := setupConnected(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Chat works initially.
	if _, err := alice.chatSvc.SendMessage(ctx, bob.addr, "before disconnect"); err != nil {
		t.Fatalf("SendMessage before disconnect: %v", err)
	}

	// Disconnect.
	alice.connector.Disconnect(bob.addr)
	pc := alice.connector.GetPeer(bob.addr)
	if pc != nil {
		t.Fatal("expected nil peer after disconnect")
	}

	// Reconnect.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()

	newPC, err := alice.connector.Connect(ctx2, bob.addr)
	if err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if newPC.State != transport.StateConnected && newPC.State != transport.StateConnectedTor {
		t.Fatalf("expected connected, got %s", newPC.State)
	}

	// Chat works after reconnect.
	if _, err := alice.chatSvc.SendMessage(ctx2, bob.addr, "after reconnect"); err != nil {
		t.Fatalf("SendMessage after reconnect: %v", err)
	}
}

func TestConnectorState_Transitions(t *testing.T) {
	alice, bob := setup(t)
	alice.addContact(bob)
	bob.addContact(alice)

	stateCh := alice.bus.Subscribe(node.EventConnectionState)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		alice.connector.Connect(ctx, bob.addr)
	}()

	// Collect state transitions.
	var states []transport.ConnState
	timeout := time.After(15 * time.Second)
	for {
		select {
		case e := <-stateCh:
			se := e.Payload.(transport.StateEvent)
			states = append(states, se.State)
			if se.State == transport.StateConnected ||
				se.State == transport.StateConnectedTor ||
				se.State == transport.StateFailed {
				goto done
			}
		case <-timeout:
			t.Fatalf("timed out waiting for states, got: %v", states)
		}
	}
done:

	// Expect at least: Discovering, Signaling, ExchangingIPs, NATTraversal|Connected
	if len(states) < 3 {
		t.Fatalf("expected at least 3 state transitions, got %d: %v", len(states), states)
	}
	if states[0] != transport.StateDiscovering {
		t.Fatalf("first state should be Discovering, got %s", states[0])
	}
	if states[1] != transport.StateSignaling {
		t.Fatalf("second state should be Signaling, got %s", states[1])
	}

	// Last state should be connected.
	last := states[len(states)-1]
	if last != transport.StateConnected && last != transport.StateConnectedTor {
		t.Fatalf("final state should be connected, got %s", last)
	}
}

// ---------------------------------------------------------------------------
// Event bus tests
// ---------------------------------------------------------------------------

func TestEventBus_ChatMessageEvent(t *testing.T) {
	alice, bob := setupConnected(t)

	ch := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice.chatSvc.SendMessage(ctx, bob.addr, "event test")

	select {
	case e := <-ch:
		if e.Type != node.EventChatMessage {
			t.Fatalf("expected EventChatMessage, got %d", e.Type)
		}
		msg := e.Payload.(*chat.Message)
		if msg.Text != "event test" {
			t.Fatalf("got %q, want %q", msg.Text, "event test")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for EventChatMessage")
	}
}

func TestEventBus_FileOfferEvent(t *testing.T) {
	alice, bob := setupConnected(t)

	offerCh := bob.bus.Subscribe(node.EventFileOffer)

	srcPath := createTestFile(t, 512)

	// Start the offer in background — it will block waiting for decision.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		alice.ftSender.OfferFile(ctx, bob.addr, srcPath)
	}()

	select {
	case e := <-offerCh:
		if e.Type != node.EventFileOffer {
			t.Fatalf("expected EventFileOffer, got %d", e.Type)
		}
		offer := e.Payload.(*filetransfer.FileOffer)
		if offer.Filename != "testfile.bin" {
			t.Fatalf("got filename %q, want %q", offer.Filename, "testfile.bin")
		}
		if offer.FromAddr != alice.addr {
			t.Fatalf("got from %q, want %q", offer.FromAddr, alice.addr)
		}
		// Reject to unblock the sender.
		offer.Decision <- filetransfer.FileDecision{Accept: false, Reason: "test"}
	case <-ctx.Done():
		t.Fatal("timed out waiting for EventFileOffer")
	}
}

func TestEventBus_ConnectionStateEvent(t *testing.T) {
	alice, bob := setup(t)
	alice.addContact(bob)
	bob.addContact(alice)

	ch := alice.bus.Subscribe(node.EventConnectionState)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		alice.connector.Connect(ctx, bob.addr)
	}()

	// Should receive at least one state event.
	select {
	case e := <-ch:
		if e.Type != node.EventConnectionState {
			t.Fatalf("expected EventConnectionState, got %d", e.Type)
		}
		se := e.Payload.(transport.StateEvent)
		if se.PeerAddr != bob.addr {
			t.Fatalf("state event for wrong peer: %s", se.PeerAddr)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for EventConnectionState")
	}
}
