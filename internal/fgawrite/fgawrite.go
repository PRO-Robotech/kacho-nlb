package fgawrite

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// ---- FGA model constants (kacho-nlb domain) ----

// ObjectType* — FGA object type strings used by the kacho-nlb authorization
// model (mirrors kacho-iam/internal/authzmap/permission_catalog.go namespace
// `loadbalancer.*`).
const (
	ObjectTypeLoadBalancer = "nlb_load_balancer"
	ObjectTypeListener     = "nlb_listener"
	ObjectTypeTargetGroup  = "nlb_target_group"
)

// Relation* — predicate names used in tuples emitted by kacho-nlb.
const (
	// RelationOwner — creator tuple "<subject> #owner @<object>".
	RelationOwner = "owner"
	// RelationProject — hierarchy tuple "project:<id> #project @<object>".
	RelationProject = "project"
	// RelationLoadBalancer — parent-link tuple
	// "nlb_load_balancer:<lb_id> #load_balancer @nlb_listener:<id>".
	RelationLoadBalancer = "load_balancer"
)

// ---- Port interface ----

// HierarchyTupleWriter — port-interface a use-case worker needs to publish FGA
// hierarchy/creator tuples. Implemented by
// `internal/clients/iam.HierarchyWriter` (composition root wires it; nil when
// the iam peer is not configured — dev/degraded mode).
//
// Methods mirror `iam.HierarchyWriter` exactly so the same adapter satisfies
// both interfaces without an extra wrapper layer.
type HierarchyTupleWriter interface {
	// WriteCreatorTuple writes "<subjectID> <relation> <object>". Used for
	// both the "owner" creator tuple and arbitrary parent-link tuples
	// (kacho-iam currently exposes a single tuple-write RPC).
	WriteCreatorTuple(ctx context.Context, subjectID, relation, object string) error

	// RewriteProjectTuple atomically (best-effort, see iam adapter doc)
	// moves the project-relation of (objectType:objectID) from srcProject to
	// dstProject. srcProject may be empty for an initial (Create-time)
	// project tuple write.
	RewriteProjectTuple(ctx context.Context, objectType, objectID, srcProject, dstProject string) error
}

// ---- Subject helpers ----

// SubjectFromPrincipal returns the FGA subject string ("<type>:<id>") for an
// authenticated principal, or "" if the principal is system / unauthenticated
// (in which case the creator-tuple emission is skipped — system-initiated
// resources have no human owner). "system" is treated as unauthenticated.
func SubjectFromPrincipal(p operations.Principal) string {
	if p.Type == "" || p.ID == "" || p.Type == "system" {
		return ""
	}
	return p.Type + ":" + p.ID
}

// ---- Emit fns (best-effort, non-fatal) ----

// EmitCreator writes "<subject> #<relation> @<objectType>:<objectID>". A nil
// writer, empty subject, or empty objectID skips the call silently (logged at
// Debug if logger present). A writer error is logged and swallowed — the
// resource row is already committed, the Operation MUST NOT fail because of
// an FGA hiccup. The D-13 lifecycle subscriber on kacho-iam side backfills
// any tuples missed here.
func EmitCreator(
	ctx context.Context,
	w HierarchyTupleWriter,
	logger *slog.Logger,
	subject, relation, objectType, objectID string,
) {
	if w == nil {
		return
	}
	if subject == "" || objectID == "" {
		if logger != nil {
			logger.Debug("fgawrite creator tuple skipped (empty subject/object)",
				"subject", subject, "object_type", objectType, "object_id", objectID)
		}
		return
	}
	object := objectType + ":" + objectID
	if err := w.WriteCreatorTuple(ctx, subject, relation, object); err != nil {
		if logger != nil {
			logger.Warn("fgawrite creator tuple write failed",
				"err", err,
				"subject", subject,
				"relation", relation,
				"object", object,
			)
		}
		return
	}
	if logger != nil {
		logger.Info("fgawrite creator tuple written",
			"subject", subject, "relation", relation, "object", object)
	}
}

// EmitParentLink writes a parent→child link tuple
// "<parentType>:<parentID> #<relation> @<childType>:<childID>". Used to attach
// a freshly created child resource (e.g. Listener) to its parent
// (e.g. LoadBalancer) so the FGA cascade `<rel> from <parent>` resolves to
// the parent's project for permission checks.
//
// Same best-effort semantics as EmitCreator.
func EmitParentLink(
	ctx context.Context,
	w HierarchyTupleWriter,
	logger *slog.Logger,
	parentType, parentID, relation, childType, childID string,
) {
	if w == nil {
		return
	}
	if parentID == "" || childID == "" {
		if logger != nil {
			logger.Debug("fgawrite parent-link tuple skipped (empty id)",
				"parent_type", parentType, "parent_id", parentID,
				"child_type", childType, "child_id", childID)
		}
		return
	}
	subject := fmt.Sprintf("%s:%s", parentType, parentID)
	object := fmt.Sprintf("%s:%s", childType, childID)
	if err := w.WriteCreatorTuple(ctx, subject, relation, object); err != nil {
		if logger != nil {
			logger.Warn("fgawrite parent-link tuple write failed",
				"err", err,
				"subject", subject,
				"relation", relation,
				"object", object,
			)
		}
		return
	}
	if logger != nil {
		logger.Info("fgawrite parent-link tuple written",
			"subject", subject, "relation", relation, "object", object)
	}
}

// EmitProjectRewrite writes/rewrites the hierarchy tuple linking a resource
// to its project: "project:<dstProject> #project @<objectType>:<objectID>".
// srcProject may be empty on the first (Create-time) write; on Move it is the
// previous project (kacho-iam handles atomicity per adapter contract).
//
// Same best-effort semantics as EmitCreator.
func EmitProjectRewrite(
	ctx context.Context,
	w HierarchyTupleWriter,
	logger *slog.Logger,
	objectType, objectID, srcProject, dstProject string,
) {
	if w == nil {
		return
	}
	if objectID == "" || dstProject == "" {
		if logger != nil {
			logger.Debug("fgawrite project rewrite skipped (empty id/dst)",
				"object_type", objectType, "object_id", objectID,
				"src_project", srcProject, "dst_project", dstProject)
		}
		return
	}
	if err := w.RewriteProjectTuple(ctx, objectType, objectID, srcProject, dstProject); err != nil {
		if logger != nil {
			logger.Warn("fgawrite project rewrite failed",
				"err", err,
				"object_type", objectType,
				"object_id", objectID,
				"src_project", srcProject,
				"dst_project", dstProject,
			)
		}
		return
	}
	if logger != nil {
		logger.Info("fgawrite project rewrite written",
			"object_type", objectType,
			"object_id", objectID,
			"src_project", srcProject,
			"dst_project", dstProject,
		)
	}
}
