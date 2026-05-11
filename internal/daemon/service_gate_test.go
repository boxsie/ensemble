package daemon

import (
	"testing"

	"github.com/boxsie/ensemble/internal/services"
	"github.com/boxsie/ensemble/internal/signaling"
)

func TestServiceConnectionGate_Public(t *testing.T) {
	svc := &services.Service{
		Name:    "echo",
		Address: "Eechosvc",
		Manifest: services.Manifest{
			ACL: services.ACLPublic,
		},
	}
	cb := serviceConnectionGate(svc)
	if got, _ := decide(cb, "Eanyone"); !got {
		t.Errorf("ACLPublic should accept any peer, got reject")
	}
}

func TestServiceConnectionGate_Allowlist(t *testing.T) {
	svc := &services.Service{
		Name:    "jeff",
		Address: "Ejeffsvc",
		Manifest: services.Manifest{
			ACL:       services.ACLAllowlist,
			Allowlist: []string{"Eauthorised1", "Eauthorised2"},
		},
	}
	cb := serviceConnectionGate(svc)

	if ok, _ := decide(cb, "Eauthorised1"); !ok {
		t.Errorf("ACLAllowlist: Eauthorised1 should be accepted")
	}
	if ok, reason := decide(cb, "Eintruder"); ok || reason != "not on allowlist" {
		t.Errorf("ACLAllowlist: Eintruder should be rejected with reason; got accepted=%v reason=%q", ok, reason)
	}
}

func TestServiceConnectionGate_AllowlistEmptyEntriesIgnored(t *testing.T) {
	// Empty allowlist entries can arrive from CSV parsers that include
	// trailing commas; they must not match a valid empty-string FromAddress.
	svc := &services.Service{
		Name: "jeff",
		Manifest: services.Manifest{
			ACL:       services.ACLAllowlist,
			Allowlist: []string{"", "Etestpeer", ""},
		},
	}
	cb := serviceConnectionGate(svc)

	if ok, _ := decide(cb, "Etestpeer"); !ok {
		t.Errorf("Etestpeer should still be accepted alongside empty entries")
	}
	if ok, _ := decide(cb, ""); ok {
		t.Errorf("empty FromAddress must not match empty allowlist entries")
	}
}

func TestServiceConnectionGate_Contacts(t *testing.T) {
	// ACLContacts on a registered service has no per-service contact store
	// today; the gate rejects with a clear reason rather than blocking.
	svc := &services.Service{
		Manifest: services.Manifest{ACL: services.ACLContacts},
	}
	cb := serviceConnectionGate(svc)
	if ok, reason := decide(cb, "Eanyone"); ok || reason != "no per-service contacts configured" {
		t.Errorf("ACLContacts: expected reject + reason, got accepted=%v reason=%q", ok, reason)
	}
}

func decide(cb func(*signaling.ConnectionRequest), from string) (bool, string) {
	req := &signaling.ConnectionRequest{
		FromAddress: from,
		Decision:    make(chan signaling.ConnectionDecision, 1),
	}
	cb(req)
	d := <-req.Decision
	return d.Accepted, d.Reason
}
