package filetransfer

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/transport"
)

// mockResolver implements PeerResolver for tests.
type mockResolver struct {
	peers map[string]*transport.PeerConnection
}

func (m *mockResolver) GetPeer(addr string) *transport.PeerConnection {
	return m.peers[addr]
}

// testPeer holds all state for one side of a file transfer test.
type testPeer struct {
	kp       *identity.Keypair
	addr     string
	host     *transport.Host
	bus      *node.EventBus
	resolver *mockResolver
	sender   *Sender
	receiver *Receiver
}

func newTestPeer(t *testing.T, saveDir string) *testPeer {
	t.Helper()
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	h, err := transport.NewHost(transport.HostConfig{Keypair: kp})
	if err != nil {
		t.Fatalf("creating host: %v", err)
	}
	t.Cleanup(func() { h.Close() })

	bus := node.NewEventBus()
	resolver := &mockResolver{peers: make(map[string]*transport.PeerConnection)}
	sender := NewSender(kp, h, resolver)
	receiver := NewReceiver(kp, h, bus, saveDir)
	t.Cleanup(func() { receiver.Close() })

	return &testPeer{
		kp:       kp,
		addr:     identity.DeriveAddress(kp.PublicKey()).String(),
		host:     h,
		bus:      bus,
		resolver: resolver,
		sender:   sender,
		receiver: receiver,
	}
}

func connectPeers(t *testing.T, a, b *testPeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.host.Connect(ctx, b.host.AddrInfo()); err != nil {
		t.Fatalf("connecting hosts: %v", err)
	}

	a.resolver.peers[b.addr] = &transport.PeerConnection{
		Address: b.addr,
		PeerID:  b.host.ID(),
	}
	b.resolver.peers[a.addr] = &transport.PeerConnection{
		Address: a.addr,
		PeerID:  a.host.ID(),
	}
}

func TestSendFileEndToEnd(t *testing.T) {
	saveDir := t.TempDir()
	alice := newTestPeer(t, t.TempDir())
	bob := newTestPeer(t, saveDir)
	connectPeers(t, alice, bob)

	// Create test file (small, 3 chunks at 1KB)
	srcData := make([]byte, 3*1024)
	for i := range srcData {
		srcData[i] = byte(i % 256)
	}
	srcPath := filepath.Join(t.TempDir(), "testfile.bin")
	os.WriteFile(srcPath, srcData, 0644)

	// Bob auto-accepts file offers
	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*FileOffer)
		offer.Decision <- FileDecision{Accept: true, SavePath: saveDir}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	xfer, err := alice.sender.OfferFile(ctx, bob.addr, srcPath)
	if err != nil {
		t.Fatalf("OfferFile() error: %v", err)
	}

	if xfer.Acked() != xfer.Total {
		t.Fatalf("not all chunks acked: %d/%d", xfer.Acked(), xfer.Total)
	}

	// Verify received file matches original
	receivedPath := filepath.Join(saveDir, "testfile.bin")
	receivedData, err := os.ReadFile(receivedPath)
	if err != nil {
		t.Fatalf("reading received file: %v", err)
	}

	srcHash := sha256.Sum256(srcData)
	rcvHash := sha256.Sum256(receivedData)
	if srcHash != rcvHash {
		t.Fatal("received file hash doesn't match original")
	}
}

func TestSendFileRejected(t *testing.T) {
	alice := newTestPeer(t, t.TempDir())
	bob := newTestPeer(t, t.TempDir())
	connectPeers(t, alice, bob)

	srcPath := createTestFile(t, 1024)

	// Bob rejects
	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*FileOffer)
		offer.Decision <- FileDecision{Accept: false, Reason: "no thanks"}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := alice.sender.OfferFile(ctx, bob.addr, srcPath)
	if err == nil {
		t.Fatal("expected error for rejected file")
	}
}

func TestSendFilePeerNotConnected(t *testing.T) {
	alice := newTestPeer(t, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := alice.sender.OfferFile(ctx, "Eunknown", "/tmp/foo.bin")
	if err == nil {
		t.Fatal("expected error for unconnected peer")
	}
}

func TestSendFileProgressCallback(t *testing.T) {
	saveDir := t.TempDir()
	alice := newTestPeer(t, t.TempDir())
	bob := newTestPeer(t, saveDir)
	connectPeers(t, alice, bob)

	// Create a file with multiple chunks (use small chunk size via custom chunker later)
	srcData := make([]byte, 5*1024)
	for i := range srcData {
		srcData[i] = byte(i % 256)
	}
	srcPath := filepath.Join(t.TempDir(), "progress.bin")
	os.WriteFile(srcPath, srcData, 0644)

	var mu sync.Mutex
	var progressUpdates []uint32

	alice.sender.SetProgressCallback(func(id string, acked, total uint32) {
		mu.Lock()
		progressUpdates = append(progressUpdates, acked)
		mu.Unlock()
	})

	// Bob auto-accepts
	offerCh := bob.bus.Subscribe(node.EventFileOffer)
	go func() {
		e := <-offerCh
		offer := e.Payload.(*FileOffer)
		offer.Decision <- FileDecision{Accept: true, SavePath: saveDir}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	xfer, err := alice.sender.OfferFile(ctx, bob.addr, srcPath)
	if err != nil {
		t.Fatalf("OfferFile() error: %v", err)
	}

	mu.Lock()
	count := len(progressUpdates)
	mu.Unlock()

	// Should have received progress updates for each chunk
	if count == 0 {
		t.Fatal("expected progress updates")
	}
	if uint32(count) != xfer.Total {
		t.Fatalf("expected %d progress updates, got %d", xfer.Total, count)
	}
}

func TestCancelTransfer(t *testing.T) {
	alice := newTestPeer(t, t.TempDir())
	bob := newTestPeer(t, t.TempDir())
	connectPeers(t, alice, bob)

	// Create a large-ish file so we have time to cancel
	srcData := make([]byte, 100*1024)
	for i := range srcData {
		srcData[i] = byte(i % 256)
	}
	srcPath := filepath.Join(t.TempDir(), "cancel.bin")
	os.WriteFile(srcPath, srcData, 0644)

	// Bob never responds (simulates slow decision), so sender will block on accept
	// We cancel before that

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := alice.sender.OfferFile(ctx, bob.addr, srcPath)
		errCh <- err
	}()

	// Wait a bit then cancel the context
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancel")
	}
}

func TestReceiverFileOfferEvent(t *testing.T) {
	alice := newTestPeer(t, t.TempDir())
	bob := newTestPeer(t, t.TempDir())
	connectPeers(t, alice, bob)

	srcPath := createTestFile(t, 1024)

	offerCh := bob.bus.Subscribe(node.EventFileOffer)

	// Bob will reject after receiving offer
	go func() {
		e := <-offerCh
		offer := e.Payload.(*FileOffer)
		if offer.Filename != filepath.Base(srcPath) {
			return
		}
		if offer.Size != 1024 {
			return
		}
		offer.Decision <- FileDecision{Accept: false, Reason: "test"}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := alice.sender.OfferFile(ctx, bob.addr, srcPath)
	if err == nil {
		t.Fatal("expected rejection error")
	}
}
