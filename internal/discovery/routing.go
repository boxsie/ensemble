package discovery

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/decred/dcrd/crypto/ripemd160"
	"github.com/mr-tron/base58"
)

const (
	// IDLength is the byte length of a NodeID (RIPEMD-160 output).
	IDLength = 20

	// NumBuckets is the number of K-buckets (one per bit of NodeID).
	NumBuckets = 160

	// K is the maximum number of peers per bucket.
	K = 20
)

// NodeID is a 20-byte identifier derived from a public key's RIPEMD-160(SHA-256) hash.
type NodeID [IDLength]byte

// String returns a hex-encoded representation of the NodeID.
func (id NodeID) String() string {
	return fmt.Sprintf("%x", id[:])
}

// NodeIDFromPublicKey derives a NodeID from an Ed25519 public key.
// Pipeline: SHA-256 → RIPEMD-160 (same as address derivation).
func NodeIDFromPublicKey(pubKey ed25519.PublicKey) NodeID {
	sha := sha256.Sum256(pubKey)
	rip := ripemd160.New()
	rip.Write(sha[:])
	var id NodeID
	copy(id[:], rip.Sum(nil))
	return id
}

// NodeIDFromAddress extracts the 20-byte hash from a Base58Check address.
func NodeIDFromAddress(addr string) (NodeID, error) {
	data, err := base58.Decode(addr)
	if err != nil {
		return NodeID{}, fmt.Errorf("decoding address: %w", err)
	}
	// version(1) + hash160(20) + checksum(4) = 25 bytes minimum.
	if len(data) < 25 {
		return NodeID{}, fmt.Errorf("address too short: %d bytes", len(data))
	}
	var id NodeID
	copy(id[:], data[1:21])
	return id, nil
}

// Distance returns the XOR distance between two NodeIDs.
func Distance(a, b NodeID) NodeID {
	var d NodeID
	for i := range d {
		d[i] = a[i] ^ b[i]
	}
	return d
}

// CommonPrefixLen returns the number of leading bits in common between two NodeIDs.
func CommonPrefixLen(a, b NodeID) int {
	for i := range IDLength {
		x := a[i] ^ b[i]
		if x == 0 {
			continue
		}
		for bit := 7; bit >= 0; bit-- {
			if x&(1<<uint(bit)) != 0 {
				return i*8 + (7 - bit)
			}
		}
	}
	return NumBuckets // identical IDs
}

// PeerInfo represents a peer in the routing table.
type PeerInfo struct {
	ID        NodeID    `json:"id"`
	Address   string    `json:"address"`
	OnionAddr string    `json:"onion_addr"`
	LastSeen  time.Time `json:"last_seen"`
}

// kBucket holds up to K peers. Most recently seen at the tail.
type kBucket struct {
	peers []*PeerInfo
}

// sourceTracker tracks insertions from a specific source for anti-Sybil.
type sourceTracker struct {
	count       int
	insertTimes []time.Time
}

// RoutingTable is a Kademlia-style routing table with 160 K-buckets.
type RoutingTable struct {
	local   NodeID
	buckets [NumBuckets]kBucket
	mu      sync.RWMutex

	// Anti-Sybil configuration (exported for testing).
	MaxPerSource int
	RateLimit    int
	RateWindow   time.Duration

	sources map[NodeID]*sourceTracker
	srcMu   sync.Mutex
}

// NewRoutingTable creates a routing table centered on the given local node ID.
func NewRoutingTable(local NodeID) *RoutingTable {
	return &RoutingTable{
		local:        local,
		MaxPerSource: 50,
		RateLimit:    10,
		RateWindow:   time.Minute,
		sources:      make(map[NodeID]*sourceTracker),
	}
}

// AddPeer adds or updates a peer in the routing table.
// source is the NodeID that told us about this peer (use the peer's own ID for self-announcements).
// Returns true if the peer was added or updated, false if rejected.
func (rt *RoutingTable) AddPeer(peer *PeerInfo, source NodeID) bool {
	if peer.ID == rt.local {
		return false
	}

	cpl := CommonPrefixLen(rt.local, peer.ID)
	if cpl >= NumBuckets {
		return false
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	bucket := &rt.buckets[cpl]

	// Check if peer already exists — update and move to tail.
	for i, p := range bucket.peers {
		if p.ID == peer.ID {
			bucket.peers[i].LastSeen = peer.LastSeen
			bucket.peers[i].OnionAddr = peer.OnionAddr
			entry := bucket.peers[i]
			bucket.peers = append(bucket.peers[:i], bucket.peers[i+1:]...)
			bucket.peers = append(bucket.peers, entry)
			return true
		}
	}

	// Anti-Sybil: check rate limits before new insertion.
	if !rt.checkRateLimit(source) {
		return false
	}

	// Bucket not full — append.
	if len(bucket.peers) < K {
		bucket.peers = append(bucket.peers, peer)
		rt.recordInsertion(source)
		return true
	}

	// Bucket full — reject (Kademlia: prefer long-lived peers).
	return false
}

// RemovePeer removes a peer by ensemble address. Returns true if found.
func (rt *RoutingTable) RemovePeer(addr string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for i := range rt.buckets {
		bucket := &rt.buckets[i]
		for j, p := range bucket.peers {
			if p.Address == addr {
				bucket.peers = append(bucket.peers[:j], bucket.peers[j+1:]...)
				return true
			}
		}
	}
	return false
}

// FindClosest returns up to count peers closest to target by XOR distance.
func (rt *RoutingTable) FindClosest(target NodeID, count int) []*PeerInfo {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var all []*PeerInfo
	for i := range rt.buckets {
		for _, p := range rt.buckets[i].peers {
			cp := *p
			all = append(all, &cp)
		}
	}

	sort.Slice(all, func(i, j int) bool {
		di := Distance(all[i].ID, target)
		dj := Distance(all[j].ID, target)
		return compareDist(di, dj) < 0
	})

	if len(all) > count {
		all = all[:count]
	}
	return all
}

// Size returns the total number of peers in the routing table.
func (rt *RoutingTable) Size() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	n := 0
	for i := range rt.buckets {
		n += len(rt.buckets[i].peers)
	}
	return n
}

// Local returns the local node's ID.
func (rt *RoutingTable) Local() NodeID {
	return rt.local
}

// AllPeers returns a copy of all peers in the routing table.
func (rt *RoutingTable) AllPeers() []*PeerInfo {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var peers []*PeerInfo
	for i := range rt.buckets {
		for _, p := range rt.buckets[i].peers {
			cp := *p
			peers = append(peers, &cp)
		}
	}
	return peers
}

// Save persists the routing table to a JSON file at the given path.
func (rt *RoutingTable) Save(path string) error {
	rt.mu.RLock()
	var peers []*PeerInfo
	for i := range rt.buckets {
		peers = append(peers, rt.buckets[i].peers...)
	}
	rt.mu.RUnlock()

	data, err := json.Marshal(peers)
	if err != nil {
		return fmt.Errorf("marshalling routing table: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// Load restores peers from a JSON file into the routing table.
// Missing file is not an error (fresh start). Peers are added without rate limiting.
func (rt *RoutingTable) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading routing table: %w", err)
	}

	var peers []*PeerInfo
	if err := json.Unmarshal(data, &peers); err != nil {
		return fmt.Errorf("unmarshalling routing table: %w", err)
	}

	// Sort by LastSeen descending so most recent peers fill buckets first.
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].LastSeen.After(peers[j].LastSeen)
	})

	rt.mu.Lock()
	defer rt.mu.Unlock()

	for _, p := range peers {
		if p.ID == rt.local {
			continue
		}
		cpl := CommonPrefixLen(rt.local, p.ID)
		if cpl >= NumBuckets {
			continue
		}
		bucket := &rt.buckets[cpl]
		if len(bucket.peers) < K {
			bucket.peers = append(bucket.peers, p)
		}
	}

	return nil
}

// checkRateLimit checks anti-Sybil rate limits for a source.
func (rt *RoutingTable) checkRateLimit(source NodeID) bool {
	rt.srcMu.Lock()
	defer rt.srcMu.Unlock()

	tracker, ok := rt.sources[source]
	if !ok {
		return true
	}

	if tracker.count >= rt.MaxPerSource {
		return false
	}

	now := time.Now()
	cutoff := now.Add(-rt.RateWindow)
	recent := 0
	for _, t := range tracker.insertTimes {
		if t.After(cutoff) {
			recent++
		}
	}
	return recent < rt.RateLimit
}

// recordInsertion records a successful insertion from a source.
func (rt *RoutingTable) recordInsertion(source NodeID) {
	rt.srcMu.Lock()
	defer rt.srcMu.Unlock()

	tracker, ok := rt.sources[source]
	if !ok {
		tracker = &sourceTracker{}
		rt.sources[source] = tracker
	}
	tracker.count++

	now := time.Now()
	tracker.insertTimes = append(tracker.insertTimes, now)

	// Prune old timestamps.
	cutoff := now.Add(-rt.RateWindow)
	pruned := tracker.insertTimes[:0]
	for _, t := range tracker.insertTimes {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	tracker.insertTimes = pruned
}

// compareDist compares two XOR distances byte-by-byte.
func compareDist(a, b NodeID) int {
	for i := range IDLength {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
