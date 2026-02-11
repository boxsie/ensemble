package discovery

import (
	"testing"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestParseMDNSEntry_Valid(t *testing.T) {
	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey()).String()

	txt := []string{
		"addr=" + addr,
		"onion=testpeer.onion",
	}

	peer := ParseMDNSEntry("myaddr", txt)
	if peer == nil {
		t.Fatal("expected valid peer")
	}
	if peer.Address != addr {
		t.Fatalf("expected address %s, got %s", addr, peer.Address)
	}
	if peer.OnionAddr != "testpeer.onion" {
		t.Fatalf("expected onion testpeer.onion, got %s", peer.OnionAddr)
	}

	expectedID, _ := NodeIDFromAddress(addr)
	if peer.ID != expectedID {
		t.Fatal("NodeID mismatch")
	}
}

func TestParseMDNSEntry_SkipSelf(t *testing.T) {
	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey()).String()

	txt := []string{
		"addr=" + addr,
		"onion=self.onion",
	}

	// Pass our own address as localAddr — should skip.
	peer := ParseMDNSEntry(addr, txt)
	if peer != nil {
		t.Fatal("should skip self")
	}
}

func TestParseMDNSEntry_MissingAddr(t *testing.T) {
	txt := []string{"onion=test.onion"}
	peer := ParseMDNSEntry("myaddr", txt)
	if peer != nil {
		t.Fatal("should return nil when addr missing")
	}
}

func TestParseMDNSEntry_MissingOnion(t *testing.T) {
	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey()).String()

	txt := []string{"addr=" + addr}
	peer := ParseMDNSEntry("myaddr", txt)
	if peer != nil {
		t.Fatal("should return nil when onion missing")
	}
}

func TestParseMDNSEntry_InvalidAddr(t *testing.T) {
	txt := []string{
		"addr=not-valid-base58",
		"onion=test.onion",
	}
	peer := ParseMDNSEntry("myaddr", txt)
	if peer != nil {
		t.Fatal("should return nil for invalid address")
	}
}

func TestParseMDNSEntry_EmptyTxt(t *testing.T) {
	peer := ParseMDNSEntry("myaddr", nil)
	if peer != nil {
		t.Fatal("should return nil for empty TXT records")
	}
}
