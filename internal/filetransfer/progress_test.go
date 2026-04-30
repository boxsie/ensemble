package filetransfer

import (
	"strings"
	"testing"
	"time"
)

func TestProgressPercent(t *testing.T) {
	p := NewProgress(1000)

	if p.Percent() != 0 {
		t.Fatalf("initial percent should be 0, got %f", p.Percent())
	}

	p.Update(500)
	if p.Percent() != 50.0 {
		t.Fatalf("expected 50%%, got %f", p.Percent())
	}

	p.Update(500)
	if p.Percent() != 100.0 {
		t.Fatalf("expected 100%%, got %f", p.Percent())
	}
}

func TestProgressComplete(t *testing.T) {
	p := NewProgress(100)

	if p.Complete() {
		t.Fatal("should not be complete initially")
	}

	p.Update(100)
	if !p.Complete() {
		t.Fatal("should be complete after all bytes")
	}
}

func TestProgressSentBytes(t *testing.T) {
	p := NewProgress(1000)

	p.Update(100)
	p.Update(200)

	if p.SentBytes() != 300 {
		t.Fatalf("expected 300 sent bytes, got %d", p.SentBytes())
	}
}

func TestProgressSpeed(t *testing.T) {
	p := NewProgress(10000)

	// Add samples with time gaps
	p.mu.Lock()
	now := time.Now()
	p.samples = []sample{
		{bytes: 1000, time: now},
		{bytes: 1000, time: now.Add(100 * time.Millisecond)},
		{bytes: 1000, time: now.Add(200 * time.Millisecond)},
		{bytes: 1000, time: now.Add(300 * time.Millisecond)},
		{bytes: 1000, time: now.Add(400 * time.Millisecond)},
	}
	p.sentBytes = 5000
	p.mu.Unlock()

	speed := p.BytesPerSecond()
	// 4000 bytes over 0.4 seconds = 10000 bytes/sec
	if speed < 9000 || speed > 11000 {
		t.Fatalf("expected ~10000 B/s, got %f", speed)
	}
}

func TestProgressETA(t *testing.T) {
	p := NewProgress(10000)

	p.mu.Lock()
	now := time.Now()
	p.samples = []sample{
		{bytes: 1000, time: now},
		{bytes: 1000, time: now.Add(time.Second)},
	}
	p.sentBytes = 2000
	p.mu.Unlock()

	eta := p.ETA()
	// 8000 bytes remaining at 1000 B/s = 8 seconds
	if eta < 7*time.Second || eta > 9*time.Second {
		t.Fatalf("expected ~8s ETA, got %v", eta)
	}
}

func TestProgressETAComplete(t *testing.T) {
	p := NewProgress(100)
	p.Update(100)

	eta := p.ETA()
	if eta != 0 {
		t.Fatalf("completed transfer should have 0 ETA, got %v", eta)
	}
}

func TestProgressString(t *testing.T) {
	p := NewProgress(10000)

	p.mu.Lock()
	now := time.Now()
	p.samples = []sample{
		{bytes: 2500, time: now},
		{bytes: 2500, time: now.Add(time.Second)},
	}
	p.sentBytes = 5000
	p.mu.Unlock()

	s := p.String()
	if !strings.Contains(s, "50%") {
		t.Fatalf("expected '50%%' in string, got %q", s)
	}
	if !strings.Contains(s, "/s") {
		t.Fatalf("expected speed in string, got %q", s)
	}
	if !strings.Contains(s, "left") {
		t.Fatalf("expected 'left' in string, got %q", s)
	}
}

func TestProgressStringComplete(t *testing.T) {
	p := NewProgress(100)

	p.mu.Lock()
	now := time.Now()
	p.samples = []sample{
		{bytes: 50, time: now},
		{bytes: 50, time: now.Add(time.Second)},
	}
	p.sentBytes = 100
	p.mu.Unlock()

	s := p.String()
	if !strings.HasPrefix(s, "100%") {
		t.Fatalf("completed string should start with '100%%', got %q", s)
	}
}

func TestProgressZeroTotal(t *testing.T) {
	p := NewProgress(0)
	if p.Percent() != 100.0 {
		t.Fatalf("zero total should be 100%%, got %f", p.Percent())
	}
}

func TestFormatSpeed(t *testing.T) {
	tests := []struct {
		input    float64
		contains string
	}{
		{500, "B/s"},
		{2048, "KB/s"},
		{5 * 1024 * 1024, "MB/s"},
		{2 * 1024 * 1024 * 1024, "GB/s"},
	}

	for _, tc := range tests {
		s := formatSpeed(tc.input)
		if !strings.Contains(s, tc.contains) {
			t.Fatalf("formatSpeed(%f) = %q, expected to contain %q", tc.input, s, tc.contains)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h30m"},
	}

	for _, tc := range tests {
		s := formatDuration(tc.input)
		if s != tc.expected {
			t.Fatalf("formatDuration(%v) = %q, expected %q", tc.input, s, tc.expected)
		}
	}
}

func TestProgressWindowTrimming(t *testing.T) {
	p := NewProgress(100000)

	// Add more samples than window size
	for i := 0; i < 50; i++ {
		p.Update(100)
	}

	p.mu.Lock()
	count := len(p.samples)
	p.mu.Unlock()

	if count > defaultWindowSize {
		t.Fatalf("samples should be trimmed to %d, got %d", defaultWindowSize, count)
	}
}
