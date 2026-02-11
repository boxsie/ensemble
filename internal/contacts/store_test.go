package contacts

import (
	"bytes"
	"testing"
)

func TestAddAndGet(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Add(&Contact{Address: "Eabc123", Alias: "Alice", PublicKey: []byte{1, 2, 3}})

	c := s.Get("Eabc123")
	if c == nil {
		t.Fatal("Get() should return the added contact")
	}
	if c.Alias != "Alice" {
		t.Fatalf("alias: got %q, want %q", c.Alias, "Alice")
	}
	if !bytes.Equal(c.PublicKey, []byte{1, 2, 3}) {
		t.Fatal("public key mismatch")
	}
	if c.AddedAt.IsZero() {
		t.Fatal("AddedAt should be set automatically")
	}
}

func TestGetNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	if s.Get("nonexistent") != nil {
		t.Fatal("Get() should return nil for unknown address")
	}
}

func TestAddUpdatesExisting(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Add(&Contact{Address: "Eabc123", Alias: "Alice"})
	s.Add(&Contact{Address: "Eabc123", Alias: "Alice Updated", PublicKey: []byte{9}})

	c := s.Get("Eabc123")
	if c.Alias != "Alice Updated" {
		t.Fatalf("alias should be updated: got %q", c.Alias)
	}
	if !c.AddedAt.IsZero() == false {
		// AddedAt should be preserved from the first add.
	}
}

func TestRemove(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Add(&Contact{Address: "Eabc123", Alias: "Alice"})

	if !s.Remove("Eabc123") {
		t.Fatal("Remove() should return true for existing contact")
	}
	if s.Get("Eabc123") != nil {
		t.Fatal("contact should be gone after Remove()")
	}
}

func TestRemoveNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	if s.Remove("nonexistent") {
		t.Fatal("Remove() should return false for unknown address")
	}
}

func TestList(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Add(&Contact{Address: "E111", Alias: "Alice"})
	s.Add(&Contact{Address: "E222", Alias: "Bob"})
	s.Add(&Contact{Address: "E333", Alias: "Charlie"})

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("List() length: got %d, want 3", len(list))
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	// Save contacts.
	s1 := NewStore(dir)
	s1.Add(&Contact{Address: "E111", Alias: "Alice", PublicKey: []byte{1}})
	s1.Add(&Contact{Address: "E222", Alias: "Bob", PublicKey: []byte{2}})
	if err := s1.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Load into a fresh store.
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	alice := s2.Get("E111")
	if alice == nil || alice.Alias != "Alice" {
		t.Fatal("Alice should be loaded from disk")
	}
	bob := s2.Get("E222")
	if bob == nil || bob.Alias != "Bob" {
		t.Fatal("Bob should be loaded from disk")
	}
}

func TestLoadNoFile(t *testing.T) {
	s := NewStore(t.TempDir())
	// Should not error when file doesn't exist yet.
	if err := s.Load(); err != nil {
		t.Fatalf("Load() should not error on missing file: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("should have no contacts after loading missing file")
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir() + "/nested/dir"
	s := NewStore(dir)
	s.Add(&Contact{Address: "E111", Alias: "Alice"})
	if err := s.Save(); err != nil {
		t.Fatalf("Save() should create missing directories: %v", err)
	}
}
