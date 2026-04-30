package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/tor"
)

// fakeOnionEngine is a deterministic in-memory stand-in for *tor.Engine.
// AddOnion returns a synthetic .onion address derived from the service name
// so tests can assert exactly which onion belongs to which service.
type fakeOnionEngine struct {
	mu       sync.Mutex
	addrs    map[string]string
	fail     map[string]error
	addCalls atomic.Int64
}

func newFakeOnionEngine() *fakeOnionEngine {
	return &fakeOnionEngine{
		addrs: make(map[string]string),
		fail:  make(map[string]error),
	}
}

func (f *fakeOnionEngine) failNext(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[name] = err
}

func (f *fakeOnionEngine) AddOnion(_ context.Context, name string, _ tor.Keypair) (string, error) {
	f.addCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.fail[name]; ok {
		delete(f.fail, name)
		return "", err
	}
	if _, ok := f.addrs[name]; ok {
		return "", tor.ErrOnionExists
	}
	addr := fmt.Sprintf("%sfakeaddr.onion", name)
	f.addrs[name] = addr
	return addr, nil
}

func (f *fakeOnionEngine) RemoveOnion(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.addrs[name]; !ok {
		return tor.ErrOnionNotFound
	}
	delete(f.addrs, name)
	return nil
}

func (f *fakeOnionEngine) running(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.addrs[name]
	return ok
}

func newTestRegistry(t *testing.T) (*Registry, *fakeOnionEngine, *identity.Keystore) {
	t.Helper()
	dir := t.TempDir()
	ks, err := identity.NewKeystore(dir, "test-passphrase")
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	te := newFakeOnionEngine()
	return New(ks, te), te, ks
}

// chatOnly implements ChatHandler.
type chatOnly struct{}

func (chatOnly) OnChatMessage(context.Context, string, string) error { return nil }

// chatAndFile implements ChatHandler and FileHandler.
type chatAndFile struct{}

func (chatAndFile) OnChatMessage(context.Context, string, string) error  { return nil }
func (chatAndFile) OnFileOffer(context.Context, string, FileOffer) error { return nil }

func TestRegisterNew(t *testing.T) {
	r, te, ks := newTestRegistry(t)
	ctx := context.Background()

	manifest := Manifest{Description: "primary", ACL: ACLContacts}
	handlers := chatOnly{}

	svc, err := r.Register(ctx, "node", manifest, handlers)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if svc.Name != "node" {
		t.Errorf("Name = %q, want %q", svc.Name, "node")
	}
	if svc.OnionAddr != "nodefakeaddr.onion" {
		t.Errorf("OnionAddr = %q, want nodefakeaddr.onion", svc.OnionAddr)
	}
	if svc.Address == "" || svc.Address[0] != 'E' {
		t.Errorf("Address = %q, want non-empty E-prefixed", svc.Address)
	}
	if svc.Identity == nil {
		t.Fatal("Identity is nil")
	}
	if svc.Manifest.Description != "primary" || svc.Manifest.ACL != ACLContacts {
		t.Errorf("Manifest mismatch: %+v", svc.Manifest)
	}
	if svc.Handlers != handlers {
		t.Errorf("Handlers reference not preserved")
	}

	if !te.running("node") {
		t.Error("expected onion to be running on the engine")
	}
	if !ks.Has("node") {
		t.Error("expected keystore to retain the node identity")
	}

	chat, ok := svc.AsChat()
	if !ok || chat == nil {
		t.Error("AsChat: handler not exposed")
	}
	if _, ok := svc.AsFile(); ok {
		t.Error("AsFile: should be false for chat-only handler")
	}
	if _, ok := svc.AsConnection(); ok {
		t.Error("AsConnection: should be false for chat-only handler")
	}
}

func TestRegisterExistingNameErrors(t *testing.T) {
	r, te, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, err := r.Register(ctx, "node", Manifest{}, nil); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	addCallsAfterFirst := te.addCalls.Load()

	_, err := r.Register(ctx, "node", Manifest{}, nil)
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register err = %v, want ErrAlreadyRegistered", err)
	}
	if got := te.addCalls.Load(); got != addCallsAfterFirst {
		t.Errorf("AddOnion called %d extra times after duplicate Register; want 0", got-addCallsAfterFirst)
	}
}

func TestUnregister(t *testing.T) {
	r, te, ks := newTestRegistry(t)
	ctx := context.Background()

	svc, err := r.Register(ctx, "node", Manifest{}, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Unregister("node"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if _, ok := r.Get("node"); ok {
		t.Error("Get after Unregister returned ok=true")
	}
	if _, ok := r.LookupByOnion(svc.OnionAddr); ok {
		t.Error("LookupByOnion after Unregister returned ok=true")
	}
	if te.running("node") {
		t.Error("onion still running after Unregister")
	}
	if !ks.Has("node") {
		t.Error("keypair was dropped from keystore on Unregister")
	}

	if err := r.Unregister("node"); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("second Unregister err = %v, want ErrNotRegistered", err)
	}
	if err := r.Unregister("never-registered"); !errors.Is(err, ErrNotRegistered) {
		t.Errorf("Unregister of unknown name err = %v, want ErrNotRegistered", err)
	}
}

func TestGetByName(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, ok := r.Get("missing"); ok {
		t.Error("Get of unknown name returned ok=true")
	}

	svc, err := r.Register(ctx, "alpha", Manifest{}, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("alpha")
	if !ok {
		t.Fatal("Get returned ok=false for registered service")
	}
	if got != svc {
		t.Error("Get returned a different *Service instance")
	}
}

func TestLookupByOnion(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	ctx := context.Background()

	alpha, err := r.Register(ctx, "alpha", Manifest{}, nil)
	if err != nil {
		t.Fatalf("Register alpha: %v", err)
	}
	beta, err := r.Register(ctx, "beta", Manifest{}, nil)
	if err != nil {
		t.Fatalf("Register beta: %v", err)
	}

	got, ok := r.LookupByOnion(alpha.OnionAddr)
	if !ok || got != alpha {
		t.Errorf("LookupByOnion(alpha): got=%v ok=%v", got, ok)
	}
	got, ok = r.LookupByOnion(beta.OnionAddr)
	if !ok || got != beta {
		t.Errorf("LookupByOnion(beta): got=%v ok=%v", got, ok)
	}

	if got, ok := r.LookupByOnion("unknown.onion"); ok {
		t.Errorf("LookupByOnion(unknown): got=%v, want miss", got)
	}

	if err := r.Unregister("alpha"); err != nil {
		t.Fatalf("Unregister alpha: %v", err)
	}
	if _, ok := r.LookupByOnion(alpha.OnionAddr); ok {
		t.Error("LookupByOnion(alpha) still hits after Unregister")
	}
	if _, ok := r.LookupByOnion(beta.OnionAddr); !ok {
		t.Error("LookupByOnion(beta) miss after unrelated Unregister")
	}
}

func TestListSortedByName(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	ctx := context.Background()

	if got := r.List(); len(got) != 0 {
		t.Errorf("empty List = %v, want empty", got)
	}

	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if _, err := r.Register(ctx, name, Manifest{}, nil); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}

	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List returned %d, want 3", len(got))
	}
	want := []string{"alpha", "bravo", "charlie"}
	for i, svc := range got {
		if svc.Name != want[i] {
			t.Errorf("List[%d].Name = %q, want %q", i, svc.Name, want[i])
		}
	}
}

func TestRegisterTorFailureLeavesNoHalfState(t *testing.T) {
	r, te, ks := newTestRegistry(t)
	ctx := context.Background()

	te.failNext("node", errors.New("simulated tor failure"))

	_, err := r.Register(ctx, "node", Manifest{}, nil)
	if err == nil {
		t.Fatal("Register: expected error, got nil")
	}

	if _, ok := r.Get("node"); ok {
		t.Error("Get returned ok=true after failed Register")
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("List = %v, want empty after failed Register", got)
	}
	if te.running("node") {
		t.Error("onion is running after failed Register")
	}
	if !ks.Has("node") {
		t.Error("keystore entry was rolled back; T01 contract is append-only")
	}

	svc, err := r.Register(ctx, "node", Manifest{}, nil)
	if err != nil {
		t.Fatalf("retry Register: %v", err)
	}
	if svc.OnionAddr == "" {
		t.Error("retry produced empty OnionAddr")
	}
}

func TestHandlerRoleAssertions(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	ctx := context.Background()

	svcChat, err := r.Register(ctx, "chatonly", Manifest{}, chatOnly{})
	if err != nil {
		t.Fatalf("Register chatonly: %v", err)
	}
	if _, ok := svcChat.AsChat(); !ok {
		t.Error("chatonly: AsChat false")
	}
	if _, ok := svcChat.AsFile(); ok {
		t.Error("chatonly: AsFile true")
	}

	svcBoth, err := r.Register(ctx, "both", Manifest{}, chatAndFile{})
	if err != nil {
		t.Fatalf("Register both: %v", err)
	}
	if _, ok := svcBoth.AsChat(); !ok {
		t.Error("both: AsChat false")
	}
	if _, ok := svcBoth.AsFile(); !ok {
		t.Error("both: AsFile false")
	}

	svcNil, err := r.Register(ctx, "nohandlers", Manifest{}, nil)
	if err != nil {
		t.Fatalf("Register nohandlers: %v", err)
	}
	if _, ok := svcNil.AsChat(); ok {
		t.Error("nohandlers: AsChat should be false for nil handlers")
	}
	if _, ok := svcNil.AsFile(); ok {
		t.Error("nohandlers: AsFile should be false for nil handlers")
	}
	if _, ok := svcNil.AsConnection(); ok {
		t.Error("nohandlers: AsConnection should be false for nil handlers")
	}
}

func TestConcurrentRegisterUnregisterStress(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	ctx := context.Background()

	const workers = 32
	const opsPerWorker = 25

	var writers sync.WaitGroup
	for w := range workers {
		writers.Add(1)
		go func(workerID int) {
			defer writers.Done()
			for i := range opsPerWorker {
				name := fmt.Sprintf("svc-%d-%d", workerID, i)
				if _, err := r.Register(ctx, name, Manifest{}, nil); err != nil {
					t.Errorf("Register %q: %v", name, err)
					return
				}
				if _, ok := r.Get(name); !ok {
					t.Errorf("Get %q: not found after Register", name)
				}
				if err := r.Unregister(name); err != nil {
					t.Errorf("Unregister %q: %v", name, err)
				}
			}
		}(w)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = r.List()
					_, _ = r.LookupByOnion("anything.onion")
				}
			}
		})
	}

	writers.Wait()
	close(stop)
	readers.Wait()

	if got := r.List(); len(got) != 0 {
		t.Errorf("after stress List = %v, want empty", got)
	}
}
