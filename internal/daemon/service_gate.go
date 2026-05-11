package daemon

import (
	"github.com/boxsie/ensemble/internal/services"
	"github.com/boxsie/ensemble/internal/signaling"
)

// serviceConnectionGate builds an OnRequest callback for a registered service
// that enforces the service's manifest-declared ACL when an unknown-peer
// CONN_REQUEST arrives. The node service uses Node.HandleConnectionRequest
// for interactive accept; registered services (jeff and friends) are bots
// with no user, so the decision is computed synchronously from the manifest.
//
// ACL semantics:
//   - ACLPublic    → always accept.
//   - ACLAllowlist → accept iff FromAddress is in the manifest's Allowlist.
//   - ACLContacts  → reject (per-service contact stores are a separate
//     follow-up; today the registered-service path has no contact store).
//
// The signaling server always consults the per-service Contacts.Store first
// (via the auto-accept path in handshake), so even though ACLContacts returns
// false here, an explicitly-configured Contacts store on the ServiceConfig
// would still produce auto-accept. The gate exists for the no-contacts case
// to give a clear decision rather than block on a missing user.
func serviceConnectionGate(svc *services.Service) func(*signaling.ConnectionRequest) {
	allowlist := make(map[string]struct{}, len(svc.Manifest.Allowlist))
	for _, addr := range svc.Manifest.Allowlist {
		if addr == "" {
			continue
		}
		allowlist[addr] = struct{}{}
	}
	acl := svc.Manifest.ACL

	return func(req *signaling.ConnectionRequest) {
		accepted := false
		reason := ""
		switch acl {
		case services.ACLPublic:
			accepted = true
		case services.ACLAllowlist:
			if _, ok := allowlist[req.FromAddress]; ok {
				accepted = true
			} else {
				reason = "not on allowlist"
			}
		case services.ACLContacts:
			reason = "no per-service contacts configured"
		default:
			reason = "unknown ACL"
		}
		req.Decision <- signaling.ConnectionDecision{Accepted: accepted, Reason: reason}
	}
}
