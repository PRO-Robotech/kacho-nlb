package domain

import (
	"encoding/json"
	"fmt"
)

// FGA-register-intent (SEC-D) — pure-Go domain value-types for the
// transactional-outbox FGA-via-IAM relay. They replace the former direct
// best-effort `internal/fgawrite` HTTP helpers (GitHub Issue N5): instead of
// writing tuples to FGA after Commit, the worker serialises the resource's
// owner-hierarchy tuples into a FGARegisterIntent and persists it in the SAME
// writer-tx as the resource INSERT/DELETE (one commit, no dual-write). A
// register-drainer later applies each tuple through kacho-iam
// InternalIAMService.RegisterResource / UnregisterResource by mTLS.
//
// This file is pure Go (stdlib only) per architecture.md domain-layer rule —
// no pgx, no grpc, no FGA HTTP client. Object-type / relation constants and
// the subject-from-principal helper that used to live in `internal/fgawrite`
// now live here as the single source of truth for the FGA authorization-model
// vocabulary kacho-nlb emits.

// FGA object-type strings of the kacho-nlb authorization model.
//
// KAC-178 §2: `lb_*` (NOT `nlb_*`) — matches the FGA model
// (`type lb_network_load_balancer / lb_listener / lb_target_group` in
// kacho-proto fga_model.fga) and api-gateway permission_catalog.json.
const (
	FGAObjectTypeLoadBalancer = "lb_network_load_balancer"
	FGAObjectTypeListener     = "lb_listener"
	FGAObjectTypeTargetGroup  = "lb_target_group"
)

// FGA relation strings emitted in kacho-nlb tuples.
//
// KAC-178 §2: creator relation is "admin" (NOT "owner"): the `lb_*` model
// defines only viewer/editor/admin. "admin" is the closest fit for creator
// semantics (full control). "project" links a resource to its project for the
// hierarchy cascade; "load_balancer" is the parent-link relation
// (lb_network_load_balancer → lb_listener).
const (
	FGARelationAdmin        = "admin"
	FGARelationProject      = "project"
	FGARelationLoadBalancer = "load_balancer"
)

// FGA register-intent event types (parity with the CHECK constraint in
// migration 0002 and with kacho-iam SEC-A RegisterResource/UnregisterResource).
const (
	FGAEventRegister   = "fga.register"
	FGAEventUnregister = "fga.unregister"
)

// FGATuple is one owner-hierarchy tuple intent "<subject_id> #<relation> @<object>".
// Field names match kacho-proto RegisterResourceRequest (subject_id / relation /
// object) so the applier maps 1:1 without translation.
type FGATuple struct {
	SubjectID string `json:"subject_id"`
	Relation  string `json:"relation"`
	Object    string `json:"object"`
}

// Valid reports whether all three components are non-empty. An incomplete tuple
// is a caller-side bug (the drainer treats a decoded incomplete tuple as a
// poison row, not a transient retry).
func (t FGATuple) Valid() bool {
	return t.SubjectID != "" && t.Relation != "" && t.Object != ""
}

// FGARegisterIntent is the full set of owner-hierarchy tuples for one resource
// (project-hierarchy + optional creator + optional parent-link). OQ-SEC-D-2:
// the whole set is one outbox row → one logical apply unit.
type FGARegisterIntent struct {
	// Kind is the resource kind for observability ("NetworkLoadBalancer" /
	// "Listener" / "TargetGroup"). NOT used for tuple application.
	Kind string `json:"kind"`
	// ResourceID is the resource id for observability/tracing. NOT used for
	// tuple application.
	ResourceID string `json:"resource_id"`
	// Tuples is the set of tuple intents to register/unregister.
	Tuples []FGATuple `json:"tuples"`
}

// Marshal serialises the intent to the JSONB payload stored in
// `kacho_nlb.fga_register_outbox`.payload. Returns an error only on the
// (impossible-for-this-shape) json.Marshal failure.
func (i FGARegisterIntent) Marshal() ([]byte, error) {
	b, err := json.Marshal(i)
	if err != nil {
		return nil, fmt.Errorf("marshal fga register intent: %w", err)
	}
	return b, nil
}

// FGAObjectRef builds the "<objectType>:<objectID>" FGA object string.
func FGAObjectRef(objectType, objectID string) string {
	return objectType + ":" + objectID
}

// FGAProjectTuple builds the project-hierarchy tuple
// "project:<projectID> #project @<objectType>:<objectID>".
func FGAProjectTuple(objectType, objectID, projectID string) FGATuple {
	return FGATuple{
		SubjectID: "project:" + projectID,
		Relation:  FGARelationProject,
		Object:    FGAObjectRef(objectType, objectID),
	}
}

// FGACreatorTuple builds the creator tuple
// "<subject> #admin @<objectType>:<objectID>". subject is the FGA subject
// string (e.g. "user:usr…") of the authenticated principal. An empty subject
// yields an invalid tuple — callers skip it (system-initiated resources have no
// human owner; parity with the old EmitCreator skip-on-empty-subject).
func FGACreatorTuple(subject, objectType, objectID string) FGATuple {
	return FGATuple{
		SubjectID: subject,
		Relation:  FGARelationAdmin,
		Object:    FGAObjectRef(objectType, objectID),
	}
}

// FGAParentLinkTuple builds the parent→child link tuple
// "<parentType>:<parentID> #<relation> @<childType>:<childID>" (e.g.
// lb_network_load_balancer:<lbID> #load_balancer @lb_listener:<id>).
func FGAParentLinkTuple(parentType, parentID, relation, childType, childID string) FGATuple {
	return FGATuple{
		SubjectID: FGAObjectRef(parentType, parentID),
		Relation:  relation,
		Object:    FGAObjectRef(childType, childID),
	}
}

// FGASubjectFromPrincipal returns the FGA subject string "<type>:<id>" for an
// authenticated principal, or "" if the principal is system/unauthenticated (in
// which case the creator-tuple is skipped). "system" is treated as
// unauthenticated. Mirrors the former fgawrite.SubjectFromPrincipal so the
// subject-string format stays in one place.
//
// Caller passes the principal's type and id (read from
// operations.PrincipalFromContext at the transport edge) — domain stays free of
// the operations import to honour the domain-layer dependency rule.
func FGASubjectFromPrincipal(principalType, principalID string) string {
	if principalType == "" || principalID == "" || principalType == "system" {
		return ""
	}
	return principalType + ":" + principalID
}
