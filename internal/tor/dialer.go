package tor

import (
	"context"
	"fmt"
	"net"

	binetor "github.com/cretz/bine/tor"
)

// Dialer dials connections through the Tor SOCKS5 proxy.
type Dialer struct {
	dialer *binetor.Dialer
}

// Dialer creates a SOCKS5 dialer that routes connections through Tor.
// The engine must be started and ready before calling this.
func (e *Engine) Dialer(ctx context.Context) (*Dialer, error) {
	if e.tor == nil {
		return nil, fmt.Errorf("tor engine not started")
	}

	d, err := e.tor.Dialer(ctx, &binetor.DialConf{
		SkipEnableNetwork: true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating tor dialer: %w", err)
	}

	return &Dialer{dialer: d}, nil
}

// DialOnion connects to a .onion address through Tor.
func (d *Dialer) DialOnion(ctx context.Context, onionAddr string, port int) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", onionAddr, port)
	conn, err := d.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}
	return conn, nil
}
