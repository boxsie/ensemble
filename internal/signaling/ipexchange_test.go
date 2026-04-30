package signaling

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	info := &IPInfo{Addrs: []string{
		"/ip4/192.168.1.1/udp/4001/quic-v1",
		"/ip6/::1/udp/4001/quic-v1",
	}}

	ct, err := EncryptIPInfo(info, alice, bob.PublicKey())
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := DecryptIPInfo(ct, bob, alice.PublicKey())
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if len(got.Addrs) != len(info.Addrs) {
		t.Fatalf("addr count: got %d, want %d", len(got.Addrs), len(info.Addrs))
	}
	for i, a := range got.Addrs {
		if a != info.Addrs[i] {
			t.Errorf("addr[%d]: got %q, want %q", i, a, info.Addrs[i])
		}
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	eve, _ := identity.Generate()

	info := &IPInfo{Addrs: []string{"/ip4/10.0.0.1/udp/5000/quic-v1"}}

	ct, err := EncryptIPInfo(info, alice, bob.PublicKey())
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = DecryptIPInfo(ct, eve, alice.PublicKey())
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key")
	}
}

func TestSharedKeySymmetric(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	aliceKey, err := deriveSharedKey(alice, bob.PublicKey())
	if err != nil {
		t.Fatalf("alice derive: %v", err)
	}

	bobKey, err := deriveSharedKey(bob, alice.PublicKey())
	if err != nil {
		t.Fatalf("bob derive: %v", err)
	}

	if len(aliceKey) != 32 {
		t.Fatalf("key length: %d", len(aliceKey))
	}
	for i := range aliceKey {
		if aliceKey[i] != bobKey[i] {
			t.Fatal("shared keys differ")
		}
	}
}

func TestCiphertextTampering(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	info := &IPInfo{Addrs: []string{"/ip4/1.2.3.4/udp/4001/quic-v1"}}
	ct, err := EncryptIPInfo(info, alice, bob.PublicKey())
	if err != nil {
		t.Fatal(err)
	}

	ct[len(ct)-1] ^= 0xff

	_, err = DecryptIPInfo(ct, bob, alice.PublicKey())
	if err == nil {
		t.Fatal("expected tampered ciphertext to fail")
	}
}

func TestCiphertextTooShort(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	_, err := DecryptIPInfo([]byte{1, 2, 3}, alice, bob.PublicKey())
	if err == nil {
		t.Fatal("expected error for short ciphertext")
	}
}

func TestEmptyAddrs(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	info := &IPInfo{Addrs: nil}
	ct, err := EncryptIPInfo(info, alice, bob.PublicKey())
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := DecryptIPInfo(ct, bob, alice.PublicKey())
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(got.Addrs) != 0 {
		t.Fatalf("expected empty addrs, got %v", got.Addrs)
	}
}

func TestExchangeIPs(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	aliceInfo := &IPInfo{Addrs: []string{"/ip4/10.0.0.1/udp/4001/quic-v1"}}
	bobInfo := &IPInfo{Addrs: []string{"/ip4/10.0.0.2/udp/4001/quic-v1"}}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		info *IPInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := ExchangeIPs(ctx, c1, aliceInfo, alice, bob.PublicKey())
		ch <- result{info, err}
	}()

	bobGot, err := ExchangeIPs(ctx, c2, bobInfo, bob, alice.PublicKey())
	if err != nil {
		t.Fatalf("bob exchange: %v", err)
	}

	aliceResult := <-ch
	if aliceResult.err != nil {
		t.Fatalf("alice exchange: %v", aliceResult.err)
	}

	if len(aliceResult.info.Addrs) != 1 || aliceResult.info.Addrs[0] != bobInfo.Addrs[0] {
		t.Errorf("alice got %v, want %v", aliceResult.info.Addrs, bobInfo.Addrs)
	}
	if len(bobGot.Addrs) != 1 || bobGot.Addrs[0] != aliceInfo.Addrs[0] {
		t.Errorf("bob got %v, want %v", bobGot.Addrs, aliceInfo.Addrs)
	}
}

func TestExchangeIPsMultipleAddrs(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	aliceInfo := &IPInfo{Addrs: []string{
		"/ip4/192.168.1.1/udp/4001/quic-v1",
		"/ip4/10.0.0.1/tcp/4001",
		"/ip6/::1/udp/4001/quic-v1",
	}}
	bobInfo := &IPInfo{Addrs: []string{
		"/ip4/172.16.0.1/udp/5000/quic-v1",
	}}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		info *IPInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := ExchangeIPs(ctx, c1, aliceInfo, alice, bob.PublicKey())
		ch <- result{info, err}
	}()

	bobGot, err := ExchangeIPs(ctx, c2, bobInfo, bob, alice.PublicKey())
	if err != nil {
		t.Fatalf("bob exchange: %v", err)
	}

	aliceResult := <-ch
	if aliceResult.err != nil {
		t.Fatalf("alice exchange: %v", aliceResult.err)
	}

	if len(bobGot.Addrs) != 3 {
		t.Errorf("bob got %d addrs, want 3", len(bobGot.Addrs))
	}
	if len(aliceResult.info.Addrs) != 1 {
		t.Errorf("alice got %d addrs, want 1", len(aliceResult.info.Addrs))
	}
}
