package filetransfer

import (
	"fmt"
	"sync"
	"time"
)

// Progress tracks real-time transfer speed, ETA, and percentage.
type Progress struct {
	mu         sync.Mutex
	totalBytes uint64
	sentBytes  uint64
	startTime  time.Time
	samples    []sample // rolling window for speed calculation
	windowSize int
}

type sample struct {
	bytes uint64
	time  time.Time
}

const defaultWindowSize = 20

// NewProgress creates a progress tracker for a transfer of the given total size.
func NewProgress(totalBytes uint64) *Progress {
	return &Progress{
		totalBytes: totalBytes,
		startTime:  time.Now(),
		windowSize: defaultWindowSize,
	}
}

// Update records that additional bytes have been transferred.
func (p *Progress) Update(bytes uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sentBytes += bytes
	now := time.Now()

	p.samples = append(p.samples, sample{bytes: bytes, time: now})

	// Trim to window size
	if len(p.samples) > p.windowSize {
		p.samples = p.samples[len(p.samples)-p.windowSize:]
	}
}

// Percent returns the transfer completion percentage (0.0 to 100.0).
func (p *Progress) Percent() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.totalBytes == 0 {
		return 100.0
	}
	return float64(p.sentBytes) / float64(p.totalBytes) * 100.0
}

// BytesPerSecond returns the rolling average transfer speed.
func (p *Progress) BytesPerSecond() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.bytesPerSecondLocked()
}

func (p *Progress) bytesPerSecondLocked() float64 {
	if len(p.samples) < 2 {
		return 0
	}

	first := p.samples[0]
	last := p.samples[len(p.samples)-1]
	elapsed := last.time.Sub(first.time).Seconds()
	if elapsed <= 0 {
		return 0
	}

	var totalBytes uint64
	// Sum all bytes except the first sample (since we measure from first sample's time)
	for _, s := range p.samples[1:] {
		totalBytes += s.bytes
	}

	return float64(totalBytes) / elapsed
}

// ETA returns the estimated time remaining.
func (p *Progress) ETA() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	speed := p.bytesPerSecondLocked()
	if speed <= 0 {
		return 0
	}

	remaining := float64(p.totalBytes) - float64(p.sentBytes)
	if remaining <= 0 {
		return 0
	}

	return time.Duration(remaining/speed) * time.Second
}

// SentBytes returns the total bytes transferred so far.
func (p *Progress) SentBytes() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sentBytes
}

// TotalBytes returns the total file size.
func (p *Progress) TotalBytes() uint64 {
	return p.totalBytes
}

// Complete returns true if all bytes have been transferred.
func (p *Progress) Complete() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sentBytes >= p.totalBytes
}

// String returns a human-readable progress string like "45% (12.3 MB/s, ~2m left)".
func (p *Progress) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	pct := float64(0)
	if p.totalBytes > 0 {
		pct = float64(p.sentBytes) / float64(p.totalBytes) * 100.0
	}

	speed := p.bytesPerSecondLocked()
	speedStr := formatSpeed(speed)

	if p.sentBytes >= p.totalBytes {
		return fmt.Sprintf("100%% (%s)", speedStr)
	}

	remaining := float64(p.totalBytes) - float64(p.sentBytes)
	if speed > 0 {
		eta := time.Duration(remaining/speed) * time.Second
		return fmt.Sprintf("%.0f%% (%s, ~%s left)", pct, speedStr, formatDuration(eta))
	}

	return fmt.Sprintf("%.0f%% (%s)", pct, speedStr)
}

func formatSpeed(bytesPerSec float64) string {
	switch {
	case bytesPerSec >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB/s", bytesPerSec/(1024*1024*1024))
	case bytesPerSec >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
	case bytesPerSec >= 1024:
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/1024)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
