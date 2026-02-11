package testutil

import (
	"testing"
	"time"
)

func TestNewTestHost(t *testing.T) {
	kp := Alice()
	th := NewTestHost(t, kp)

	if th.Host == nil {
		t.Fatal("host should not be nil")
	}
	if th.Keypair != kp {
		t.Fatal("keypair mismatch")
	}
	if th.Host.ID() == "" {
		t.Fatal("host ID should not be empty")
	}
	if len(th.Host.Addrs()) == 0 {
		t.Fatal("host should have at least one address")
	}
}

func TestNewConnectedPair(t *testing.T) {
	a, b := NewConnectedPair(t)

	// Verify they can reach each other.
	conns := a.Host.Inner().Network().ConnsToPeer(b.Host.ID())
	if len(conns) == 0 {
		t.Fatal("hosts should be connected")
	}
}

func TestContextWithTimeout(t *testing.T) {
	ctx := ContextWithTimeout(t, 5*time.Second)
	if ctx.Err() != nil {
		t.Fatal("context should not be cancelled yet")
	}
}

func TestWaitFor(t *testing.T) {
	counter := 0
	WaitFor(t, 1*time.Second, func() bool {
		counter++
		return counter >= 3
	}, "counter to reach 3")

	if counter < 3 {
		t.Fatalf("counter should be >= 3, got %d", counter)
	}
}

func TestShortAddr(t *testing.T) {
	addr := AliceAddr()
	short := ShortAddr(addr)
	if len(short) > 11 { // 8 chars + "..."
		t.Fatalf("short addr too long: %s", short)
	}
	if ShortAddr("abc") != "abc" {
		t.Fatal("short addr should not truncate short strings")
	}
}

func TestMustGenerate(t *testing.T) {
	kp := MustGenerate(t)
	if kp == nil {
		t.Fatal("keypair should not be nil")
	}
}
