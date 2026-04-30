package contacts

import "time"

// Contact represents a known peer.
type Contact struct {
	Address    string    `json:"address"`
	Alias      string    `json:"alias"`
	PublicKey  []byte    `json:"public_key"`
	OnionAddr  string    `json:"onion_address"`
	AddedAt    time.Time `json:"added_at"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
}
