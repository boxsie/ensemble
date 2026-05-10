package discovery

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
)

// generateTestPeer creates a PeerInfo with a random keypair.
func generateTestPeer(t *testing.T) *PeerInfo {
	t.Helper()
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	addr := identity.DeriveAddress(kp.PublicKey())
	return &PeerInfo{
		ID:        NodeIDFromPublicKey(kp.PublicKey()),
		Address:   addr.String(),
		OnionAddr: "test" + addr.Short() + ".onion",
		LastSeen:  time.Now(),
	}
}

// localID creates a deterministic NodeID for the local node in tests.
func localID(t *testing.T) (NodeID, ed25519.PublicKey) {
	t.Helper()
	seed := make([]byte, 32)
	seed[0] = 0xFF // just needs to be deterministic
	kp, err := identity.FromSeed(seed)
	if err != nil {
		t.Fatalf("creating local keypair: %v", err)
	}
	return NodeIDFromPublicKey(kp.PublicKey()), kp.PublicKey()
}

func TestNodeIDFromPublicKey_Deterministic(t *testing.T) {
	kp, _ := identity.Generate()
	id1 := NodeIDFromPublicKey(kp.PublicKey())
	id2 := NodeIDFromPublicKey(kp.PublicKey())
	if id1 != id2 {
		t.Fatal("same public key produced different NodeIDs")
	}
}

func TestNodeIDFromPublicKey_MatchesAddress(t *testing.T) {
	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey())

	idFromKey := NodeIDFromPublicKey(kp.PublicKey())
	idFromAddr, err := NodeIDFromAddress(addr.String())
	if err != nil {
		t.Fatalf("NodeIDFromAddress: %v", err)
	}

	if idFromKey != idFromAddr {
		t.Fatalf("NodeID from public key %s != NodeID from address %s", idFromKey, idFromAddr)
	}
}

func TestNodeIDFromAddress_Invalid(t *testing.T) {
	_, err := NodeIDFromAddress("not-a-valid-address!!!")
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
}

func TestDistance_Symmetry(t *testing.T) {
	kp1, _ := identity.Generate()
	kp2, _ := identity.Generate()
	a := NodeIDFromPublicKey(kp1.PublicKey())
	b := NodeIDFromPublicKey(kp2.PublicKey())

	if Distance(a, b) != Distance(b, a) {
		t.Fatal("XOR distance is not symmetric")
	}
}

func TestDistance_Identity(t *testing.T) {
	kp, _ := identity.Generate()
	a := NodeIDFromPublicKey(kp.PublicKey())
	d := Distance(a, a)

	var zero NodeID
	if d != zero {
		t.Fatal("distance to self should be zero")
	}
}

func TestCommonPrefixLen(t *testing.T) {
	var a, b NodeID

	// Identical — CPL = 160.
	if cpl := CommonPrefixLen(a, a); cpl != NumBuckets {
		t.Fatalf("identical IDs: expected CPL %d, got %d", NumBuckets, cpl)
	}

	// Differ at first bit.
	a[0] = 0x00
	b[0] = 0x80
	if cpl := CommonPrefixLen(a, b); cpl != 0 {
		t.Fatalf("first bit differs: expected CPL 0, got %d", cpl)
	}

	// Differ at 9th bit (second byte, first bit).
	a[0] = 0x00
	b[0] = 0x00
	a[1] = 0x00
	b[1] = 0x80
	if cpl := CommonPrefixLen(a, b); cpl != 8 {
		t.Fatalf("9th bit differs: expected CPL 8, got %d", cpl)
	}

	// Differ at last bit.
	a = NodeID{}
	b = NodeID{}
	b[IDLength-1] = 0x01
	if cpl := CommonPrefixLen(a, b); cpl != 159 {
		t.Fatalf("last bit differs: expected CPL 159, got %d", cpl)
	}
}

func TestAddPeer_Basic(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	ok := rt.AddPeer(peer, peer.ID)
	if !ok {
		t.Fatal("AddPeer should succeed")
	}
	if rt.Size() != 1 {
		t.Fatalf("expected size 1, got %d", rt.Size())
	}
}

func TestAddPeer_RejectSelf(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	self := &PeerInfo{ID: lid, Address: "self", LastSeen: time.Now()}
	ok := rt.AddPeer(self, lid)
	if ok {
		t.Fatal("should reject local node ID")
	}
}

func TestAddPeer_UpdateExisting(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	rt.AddPeer(peer, peer.ID)

	// Update with new onion address and later timestamp.
	updated := &PeerInfo{
		ID:        peer.ID,
		Address:   peer.Address,
		OnionAddr: "updated.onion",
		LastSeen:  time.Now().Add(time.Hour),
	}
	ok := rt.AddPeer(updated, peer.ID)
	if !ok {
		t.Fatal("update should succeed")
	}
	if rt.Size() != 1 {
		t.Fatal("update should not increase size")
	}

	found := rt.FindClosest(peer.ID, 1)
	if len(found) != 1 || found[0].OnionAddr != "updated.onion" {
		t.Fatal("onion address should be updated")
	}
}

func TestAddPeer_BucketFull(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	// Generate K+1 peers that all land in the same bucket.
	// To control the bucket, we craft NodeIDs that differ from local at the same bit.
	added := 0
	attempts := 0
	var targetCPL int
	var firstPeer *PeerInfo

	for added <= K && attempts < 10000 {
		peer := generateTestPeer(t)
		cpl := CommonPrefixLen(lid, peer.ID)
		if cpl >= NumBuckets {
			attempts++
			continue
		}
		if added == 0 {
			targetCPL = cpl
			firstPeer = peer
		}
		if cpl != targetCPL {
			attempts++
			continue
		}
		ok := rt.AddPeer(peer, peer.ID)
		if added < K {
			if !ok {
				t.Fatalf("peer %d should have been added", added)
			}
		} else {
			// K+1th peer — bucket should be full.
			if ok {
				t.Fatal("bucket should be full, peer should be rejected")
			}
		}
		added++
		attempts++
	}

	_ = firstPeer
	if added <= K {
		t.Skipf("could not generate enough peers for bucket %d (got %d)", targetCPL, added)
	}
}

func TestRemovePeer(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	rt.AddPeer(peer, peer.ID)

	if !rt.RemovePeer(peer.Address) {
		t.Fatal("RemovePeer should return true for existing peer")
	}
	if rt.Size() != 0 {
		t.Fatal("size should be 0 after removal")
	}
	if rt.RemovePeer(peer.Address) {
		t.Fatal("RemovePeer should return false for absent peer")
	}
}

func TestFindClosest(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	// Add 50 random peers.
	peers := make([]*PeerInfo, 50)
	for i := range peers {
		peers[i] = generateTestPeer(t)
		rt.AddPeer(peers[i], peers[i].ID)
	}

	// Pick a random target.
	targetKey := make([]byte, 32)
	rand.Read(targetKey)
	target := NodeIDFromPublicKey(ed25519.PublicKey(targetKey))

	closest := rt.FindClosest(target, 10)
	if len(closest) > 10 {
		t.Fatalf("expected at most 10, got %d", len(closest))
	}

	// Verify sorted by distance.
	for i := 1; i < len(closest); i++ {
		d1 := Distance(closest[i-1].ID, target)
		d2 := Distance(closest[i].ID, target)
		if compareDist(d1, d2) > 0 {
			t.Fatalf("result %d is farther than result %d", i-1, i)
		}
	}
}

func TestFindClosest_ReturnsCorrectPeers(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	// Add 30 random peers.
	for range 30 {
		p := generateTestPeer(t)
		rt.AddPeer(p, p.ID)
	}

	// Add a peer we know the ID of.
	known := generateTestPeer(t)
	rt.AddPeer(known, known.ID)

	// Search for that exact peer — it should be first result.
	closest := rt.FindClosest(known.ID, 5)
	if len(closest) == 0 {
		t.Fatal("expected at least one result")
	}
	if closest[0].ID != known.ID {
		t.Fatal("closest peer to itself should be first")
	}
}

func TestRateLimit_PerSourceCap(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)
	rt.MaxPerSource = 5
	rt.RateLimit = 1000 // high limit so only total cap matters
	rt.RateWindow = time.Hour

	// A single malicious source tries to insert many peers.
	var source NodeID
	source[0] = 0xAA

	added := 0
	for range 100 {
		peer := generateTestPeer(t)
		if rt.AddPeer(peer, source) {
			added++
		}
	}

	if added > rt.MaxPerSource {
		t.Fatalf("source should be capped at %d, but added %d", rt.MaxPerSource, added)
	}
}

func TestRateLimit_WindowCap(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)
	rt.MaxPerSource = 1000 // high total so only window matters
	rt.RateLimit = 3
	rt.RateWindow = time.Hour // all inserts are within window

	var source NodeID
	source[0] = 0xBB

	added := 0
	for range 100 {
		peer := generateTestPeer(t)
		if rt.AddPeer(peer, source) {
			added++
		}
	}

	if added > rt.RateLimit {
		t.Fatalf("rate limit should cap at %d per window, but added %d", rt.RateLimit, added)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	// Add some peers.
	for range 20 {
		p := generateTestPeer(t)
		rt.AddPeer(p, p.ID)
	}
	originalSize := rt.Size()

	// Save.
	path := filepath.Join(t.TempDir(), "routing.json")
	if err := rt.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into a fresh table.
	rt2 := NewRoutingTable(lid)
	if err := rt2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if rt2.Size() != originalSize {
		t.Fatalf("expected %d peers after load, got %d", originalSize, rt2.Size())
	}
}

func TestLoad_MissingFile(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	err := rt.Load("/nonexistent/path/routing.json")
	if err != nil {
		t.Fatalf("missing file should not error, got: %v", err)
	}
	if rt.Size() != 0 {
		t.Fatal("expected empty table")
	}
}

func TestSaveLoad_MostRecentFirst(t *testing.T) {
	lid, _ := localID(t)

	// Create a table with many peers in the same bucket to test ordering.
	// We'll add K peers with specific last-seen times, save, then load
	// into a table where the bucket is limited — most recent should survive.
	rt := NewRoutingTable(lid)

	var peers []*PeerInfo
	for i := range 40 {
		p := generateTestPeer(t)
		p.LastSeen = time.Now().Add(time.Duration(i) * time.Second)
		rt.AddPeer(p, p.ID)
		peers = append(peers, p)
	}

	path := filepath.Join(t.TempDir(), "routing.json")
	if err := rt.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into fresh table — should prefer most recently seen.
	rt2 := NewRoutingTable(lid)
	if err := rt2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if rt2.Size() == 0 {
		t.Fatal("expected peers after load")
	}
}

func TestAllPeers_Empty(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peers := rt.AllPeers()
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(peers))
	}
}

func TestAllPeers_ReturnsCopy(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	p1 := generateTestPeer(t)
	p2 := generateTestPeer(t)
	p3 := generateTestPeer(t)
	rt.AddPeer(p1, p1.ID)
	rt.AddPeer(p2, p2.ID)
	rt.AddPeer(p3, p3.ID)

	peers := rt.AllPeers()
	if len(peers) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(peers))
	}

	// Verify all original addresses are present.
	addrs := make(map[string]bool)
	for _, p := range peers {
		addrs[p.Address] = true
	}
	for _, p := range []*PeerInfo{p1, p2, p3} {
		if !addrs[p.Address] {
			t.Fatalf("missing peer %s", p.Address)
		}
	}

	// Mutating returned slice should not affect routing table.
	peers[0].OnionAddr = "mutated.onion"
	rtPeers := rt.AllPeers()
	for _, p := range rtPeers {
		if p.OnionAddr == "mutated.onion" {
			t.Fatal("AllPeers should return copies, not pointers to internal data")
		}
	}
}

func TestEvict_DropsOnlyOlderThanMaxAge(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	now := time.Now()

	fresh := generateTestPeer(t)
	fresh.LastSeen = now.Add(-1 * time.Hour) // well within window
	rt.AddPeer(fresh, fresh.ID)

	stale := generateTestPeer(t)
	stale.LastSeen = now.Add(-200 * time.Hour) // older than 7 days
	rt.AddPeer(stale, stale.ID)

	borderline := generateTestPeer(t)
	borderline.LastSeen = now.Add(-167 * time.Hour) // just inside 168h
	rt.AddPeer(borderline, borderline.ID)

	if rt.Size() != 3 {
		t.Fatalf("expected 3 peers before evict, got %d", rt.Size())
	}

	evicted := rt.Evict(168 * time.Hour)
	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}
	if rt.Size() != 2 {
		t.Fatalf("expected 2 peers after evict, got %d", rt.Size())
	}

	// Confirm the right peer survived.
	all := rt.AllPeers()
	survived := map[string]bool{}
	for _, p := range all {
		survived[p.Address] = true
	}
	if survived[stale.Address] {
		t.Fatal("stale peer should have been evicted")
	}
	if !survived[fresh.Address] || !survived[borderline.Address] {
		t.Fatal("fresh and borderline peers should have survived")
	}
}

func TestEvict_ZeroLastSeenIsAncient(t *testing.T) {
	// PeerInfo.LastSeen zero time → treated as expired/ancient. This matches
	// the existing isExpired convention and protects against records that
	// slipped in without a LastSeen stamp.
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	zeroLS := generateTestPeer(t)
	zeroLS.LastSeen = time.Time{}
	rt.AddPeer(zeroLS, zeroLS.ID)

	fresh := generateTestPeer(t)
	fresh.LastSeen = time.Now()
	rt.AddPeer(fresh, fresh.ID)

	evicted := rt.Evict(168 * time.Hour)
	if evicted != 1 {
		t.Fatalf("expected 1 evicted (zero LastSeen), got %d", evicted)
	}
	if rt.Size() != 1 {
		t.Fatalf("expected 1 surviving peer, got %d", rt.Size())
	}
}

func TestEvict_NoMaxAgeStillDropsZeroLastSeen(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	zeroLS := generateTestPeer(t)
	zeroLS.LastSeen = time.Time{}
	rt.AddPeer(zeroLS, zeroLS.ID)

	fresh := generateTestPeer(t)
	fresh.LastSeen = time.Now().Add(-2000 * time.Hour) // would be very stale
	rt.AddPeer(fresh, fresh.ID)

	// maxAge=0 disables time-based eviction but zero-time peers still go.
	evicted := rt.Evict(0)
	if evicted != 1 {
		t.Fatalf("expected 1 evicted (zero LastSeen), got %d", evicted)
	}
	if rt.Size() != 1 {
		t.Fatalf("expected 1 surviving peer, got %d", rt.Size())
	}
}

func TestRecordFailureSuccess_Counters(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	rt.AddPeer(peer, peer.ID)

	if got := rt.RecordFailure(peer.OnionAddr); got != 1 {
		t.Fatalf("expected 1 after first failure, got %d", got)
	}
	if got := rt.RecordFailure(peer.OnionAddr); got != 2 {
		t.Fatalf("expected 2 after second failure, got %d", got)
	}

	rt.RecordSuccess(peer.OnionAddr)

	if got := rt.RecordFailure(peer.OnionAddr); got != 1 {
		t.Fatalf("expected counter reset to 1 after success+failure, got %d", got)
	}

	// Unknown peer → returns 0 sentinel, no panic.
	if got := rt.RecordFailure("nonexistent.onion"); got != 0 {
		t.Fatalf("expected 0 for unknown peer, got %d", got)
	}
}

func TestEvictByFailures_DropsAtThreshold(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	doomed := generateTestPeer(t)
	rt.AddPeer(doomed, doomed.ID)

	survivor := generateTestPeer(t)
	rt.AddPeer(survivor, survivor.ID)

	// 3 consecutive failures on doomed.
	rt.RecordFailure(doomed.OnionAddr)
	rt.RecordFailure(doomed.OnionAddr)
	rt.RecordFailure(doomed.OnionAddr)
	// 1 failure on survivor — under threshold.
	rt.RecordFailure(survivor.OnionAddr)

	dropped := rt.EvictByFailures(3)
	if len(dropped) != 1 {
		t.Fatalf("expected 1 dropped, got %d", len(dropped))
	}
	if dropped[0].Address != doomed.Address {
		t.Fatalf("expected doomed peer to be dropped, got %s", dropped[0].Address)
	}
	if rt.Size() != 1 {
		t.Fatalf("expected 1 peer remaining, got %d", rt.Size())
	}
}

func TestEvictByFailures_TwoFailuresPlusSuccessResets(t *testing.T) {
	// Acceptance criterion: 2 failures + 1 success resets the counter; the
	// peer should NOT be evicted on a subsequent third failure.
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	rt.AddPeer(peer, peer.ID)

	rt.RecordFailure(peer.OnionAddr)
	rt.RecordFailure(peer.OnionAddr)
	rt.RecordSuccess(peer.OnionAddr) // reset

	if dropped := rt.EvictByFailures(3); len(dropped) != 0 {
		t.Fatalf("after success reset, no peer should be evicted; got %d", len(dropped))
	}

	// One more failure brings counter to 1 — still under threshold.
	rt.RecordFailure(peer.OnionAddr)
	if dropped := rt.EvictByFailures(3); len(dropped) != 0 {
		t.Fatalf("counter should be 1 after reset+1 failure; got dropped=%d", len(dropped))
	}
	if rt.Size() != 1 {
		t.Fatalf("peer should still be in routing table, size=%d", rt.Size())
	}
}

func TestEvictByFailures_ZeroThresholdDisabled(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	rt.AddPeer(peer, peer.ID)
	for i := 0; i < 10; i++ {
		rt.RecordFailure(peer.OnionAddr)
	}

	dropped := rt.EvictByFailures(0)
	if len(dropped) != 0 {
		t.Fatalf("threshold=0 should disable eviction, got %d dropped", len(dropped))
	}
	if rt.Size() != 1 {
		t.Fatal("peer should still be present")
	}
}

func TestFailureCounter_NotPersisted(t *testing.T) {
	// Save/Load round trip must NOT carry the in-memory failure counter.
	// Restart should give every peer a fresh shot.
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	peer := generateTestPeer(t)
	rt.AddPeer(peer, peer.ID)
	rt.RecordFailure(peer.OnionAddr)
	rt.RecordFailure(peer.OnionAddr)

	path := filepath.Join(t.TempDir(), "routing.json")
	if err := rt.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rt2 := NewRoutingTable(lid)
	if err := rt2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	loaded := rt2.AllPeers()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded peer, got %d", len(loaded))
	}
	if loaded[0].Failures != 0 {
		t.Fatalf("failure counter must not survive save/load; got %d", loaded[0].Failures)
	}
}

func TestConcurrentAddPeer(t *testing.T) {
	lid, _ := localID(t)
	rt := NewRoutingTable(lid)

	done := make(chan struct{})
	for range 10 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 50 {
				p := generateTestPeer(t)
				rt.AddPeer(p, p.ID)
			}
		}()
	}
	for range 10 {
		<-done
	}

	if rt.Size() == 0 {
		t.Fatal("expected peers after concurrent adds")
	}
}
