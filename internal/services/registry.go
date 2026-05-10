package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/boxsie/ensemble/internal/identity"
)

// ErrAlreadyRegistered is returned by Register when a service with the given
// name is already in the registry. The registry refuses to silently
// double-provision an onion; callers must Unregister first if they want to
// re-register a name.
var ErrAlreadyRegistered = errors.New("services: service already registered")

// ErrNotRegistered is returned by Unregister and Get-style helpers when no
// service with that name (or onion address) is in the registry.
var ErrNotRegistered = errors.New("services: service not registered")

// Discovery is the slice of *discovery.Manager that the registry depends on
// to advertise per-service identities to the DHT. Defining the interface here
// keeps the registry free of any discovery import (it must not depend on a
// package that one day might want to depend on services).
type Discovery interface {
	AddLocalIdentity(addr, onion string) error
	RemoveLocalIdentity(addr string)
}

// Registry tracks the set of services running on the daemon. Methods are
// safe for concurrent use. The slow Tor work performed during Register and
// Unregister is done with the registry mutex released so unrelated lookups
// are not blocked while an onion descriptor publishes.
type Registry struct {
	ks *identity.Keystore
	te OnionEngine

	mu      sync.RWMutex
	byName  map[string]*Service
	byOnion map[string]*Service
	disc    Discovery
}

// New constructs a Registry that uses ks for keypair persistence and te for
// onion provisioning. Both must be non-nil; the constructor does not validate
// this so the caller is responsible for wiring them up correctly.
func New(ks *identity.Keystore, te OnionEngine) *Registry {
	return &Registry{
		ks:      ks,
		te:      te,
		byName:  make(map[string]*Service),
		byOnion: make(map[string]*Service),
	}
}

// Register provisions a service: load-or-generate its keypair, publish an
// onion under the same name, derive the address, and store the immutable
// Service record. The handlers value may be nil (a service with no inbound
// dispatch) or any value implementing one or more of ChatHandler,
// FileHandler, or ConnectionHandler.
//
// Returns ErrAlreadyRegistered if a service with this name is already in the
// registry. If onion provisioning fails the keypair stays in the keystore
// (keystore is append-only by design) but no Service record is added.
func (r *Registry) Register(ctx context.Context, name string, manifest Manifest, handlers any) (*Service, error) {
	r.mu.RLock()
	_, exists := r.byName[name]
	r.mu.RUnlock()
	if exists {
		return nil, ErrAlreadyRegistered
	}

	kp, err := r.ks.GetOrGenerate(name)
	if err != nil {
		return nil, fmt.Errorf("loading identity for %q: %w", name, err)
	}

	onionAddr, err := r.te.AddOnion(ctx, name, nil)
	if err != nil {
		return nil, fmt.Errorf("publishing onion for %q: %w", name, err)
	}

	svc := &Service{
		Name:      name,
		Address:   string(identity.DeriveAddress(kp.PublicKey())),
		OnionAddr: onionAddr,
		Identity:  kp,
		Manifest:  manifest,
		Handlers:  handlers,
	}

	r.mu.Lock()
	if _, ok := r.byName[name]; ok {
		r.mu.Unlock()
		if remErr := r.te.RemoveOnion(name); remErr != nil {
			return nil, fmt.Errorf("rolling back onion for %q after race: %w", name, remErr)
		}
		return nil, ErrAlreadyRegistered
	}
	r.byName[name] = svc
	r.byOnion[onionAddr] = svc
	disc := r.disc
	r.mu.Unlock()

	if disc != nil {
		if err := disc.AddLocalIdentity(svc.Address, svc.OnionAddr); err != nil {
			r.mu.Lock()
			delete(r.byName, name)
			delete(r.byOnion, onionAddr)
			r.mu.Unlock()
			if remErr := r.te.RemoveOnion(name); remErr != nil {
				return nil, fmt.Errorf("advertising %q: %w (rollback also failed: %v)", name, err, remErr)
			}
			return nil, fmt.Errorf("advertising %q: %w", name, err)
		}
	}

	return svc, nil
}

// Unregister tears down the running onion for name and removes the Service
// from the registry. The keypair is retained in the keystore. Returns
// ErrNotRegistered if no such service is in the registry.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	svc, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return ErrNotRegistered
	}
	delete(r.byName, name)
	delete(r.byOnion, svc.OnionAddr)
	disc := r.disc
	r.mu.Unlock()

	if disc != nil {
		disc.RemoveLocalIdentity(svc.Address)
	}

	if err := r.te.RemoveOnion(name); err != nil {
		return fmt.Errorf("removing onion for %q: %w", name, err)
	}
	return nil
}

// SetDiscovery wires a Discovery implementation into the registry so future
// Register/Unregister calls advertise per-service identities to the DHT. It
// also retroactively advertises any services already registered (this matters
// because Tor and the registry come up before the discovery manager). Passing
// nil unwires.
func (r *Registry) SetDiscovery(d Discovery) {
	r.mu.Lock()
	r.disc = d
	var existing []*Service
	if d != nil {
		existing = make([]*Service, 0, len(r.byName))
		for _, svc := range r.byName {
			existing = append(existing, svc)
		}
	}
	r.mu.Unlock()

	for _, svc := range existing {
		if err := d.AddLocalIdentity(svc.Address, svc.OnionAddr); err != nil {
			// Log via the caller's path; registry stays mute by design.
			_ = err
		}
	}
}

// Get returns the Service registered under name. The bool is false when no
// such service is registered.
func (r *Registry) Get(name string) (*Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.byName[name]
	return svc, ok
}

// LookupByOnion returns the Service whose OnionAddr matches onionAddr. The
// bool is false when no service has that onion. This is the hot path for
// the signaling layer's per-service routing demux (T04) and is O(1).
func (r *Registry) LookupByOnion(onionAddr string) (*Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.byOnion[onionAddr]
	return svc, ok
}

// List returns every registered service, sorted by name.
func (r *Registry) List() []*Service {
	r.mu.RLock()
	out := make([]*Service, 0, len(r.byName))
	for _, svc := range r.byName {
		out = append(out, svc)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}
