package contacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store manages contacts with JSON file persistence.
type Store struct {
	mu       sync.RWMutex
	contacts map[string]*Contact // keyed by address
	path     string
}

// NewStore creates a store that persists to the given directory.
// The contacts file will be stored as "contacts.json" inside dir.
func NewStore(dir string) *Store {
	return &Store{
		contacts: make(map[string]*Contact),
		path:     filepath.Join(dir, "contacts.json"),
	}
}

// Add adds or updates a contact.
func (s *Store) Add(c *Contact) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.contacts[c.Address]; ok {
		existing.Alias = c.Alias
		existing.PublicKey = c.PublicKey
		existing.OnionAddr = c.OnionAddr
	} else {
		c.AddedAt = time.Now()
		s.contacts[c.Address] = c
	}
}

// Remove deletes a contact by address. Returns false if not found.
func (s *Store) Remove(address string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.contacts[address]; !ok {
		return false
	}
	delete(s.contacts, address)
	return true
}

// Get returns a contact by address, or nil if not found.
func (s *Store) Get(address string) *Contact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.contacts[address]
	if !ok {
		return nil
	}
	// Return a copy to avoid data races.
	cp := *c
	return &cp
}

// List returns all contacts.
func (s *Store) List() []*Contact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*Contact, 0, len(s.contacts))
	for _, c := range s.contacts {
		cp := *c
		list = append(list, &cp)
	}
	return list
}

// Save writes all contacts to disk as JSON.
func (s *Store) Save() error {
	s.mu.RLock()
	list := make([]*Contact, 0, len(s.contacts))
	for _, c := range s.contacts {
		list = append(list, c)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling contacts: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0600); err != nil {
		return fmt.Errorf("writing contacts file: %w", err)
	}
	return nil
}

// Load reads contacts from disk.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no file yet — not an error
		}
		return fmt.Errorf("reading contacts file: %w", err)
	}

	var list []*Contact
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("unmarshalling contacts: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.contacts = make(map[string]*Contact, len(list))
	for _, c := range list {
		s.contacts[c.Address] = c
	}
	return nil
}
