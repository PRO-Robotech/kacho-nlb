// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// register_applier.go — register-drainer applier over kacho-iam
// InternalIAMService.RegisterResource / UnregisterResource.
//
// This is the kacho-nlb half of the fga_register_outbox drainer (Вариант A).
// It replaces the former direct best-effort FGA write (`internal/fgawrite` +
// iam.HierarchyWriter.WriteCreatorTuple after Commit). The worker now persists
// a FGARegisterIntent in the
// resource writer-tx; this applier drains each row and applies its tuple set
// through kacho-iam by mTLS, mapping the gRPC reply onto the drainer's
// three-way classification:
//
//	nil                       → drainer marks sent_at (happy path)
//	drainer.ErrAlreadyApplied → drainer marks sent_at (idempotent success)
//	drainer.ErrPermanent      → drainer poisons the row (attempt_count = Max)
//	anything else             → drainer retries with exp backoff (transient)
//
// RegisterResource / UnregisterResource are idempotent by contract
// (repeat owner-tuple → OK, NOT AlreadyExists; delete missing → OK), so the
// happy path returns nil even on replay. codes.Unavailable /
// DeadlineExceeded are transient → the intent stays durable and is retried
// after IAM recovers (fail-closed). codes.InvalidArgument is a
// malformed-tuple poison. The set of tuples in one intent is applied
// as a unit; a transient failure on any tuple retries the whole row (at-least-
// once + IAM idempotency = exactly-once effect).
package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
	iampb "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// RegisterResourceClient — narrow port the register-applier needs from the
// kacho-iam Internal FGA-proxy. Implemented by the generated
// InternalIAMServiceClient; a fake in tests records calls and scripts replies.
//
// Defined here (consumer side) so use-case / drainer code depends on the port,
// not the grpc stub (architecture.md dependency rule). The composition root
// wires the concrete stub over a (possibly mTLS) conn.
type RegisterResourceClient interface {
	RegisterResource(ctx context.Context, in *iampb.RegisterResourceRequest, opts ...grpc.CallOption) (*iampb.RegisterResourceResponse, error)
	UnregisterResource(ctx context.Context, in *iampb.UnregisterResourceRequest, opts ...grpc.CallOption) (*iampb.UnregisterResourceResponse, error)
}

// NewRegisterResourceClient wraps a grpc conn (to the kacho-iam INTERNAL
// listener :9091 — RegisterResource/UnregisterResource are Internal-only)
// into the RegisterResourceClient port. nil conn → nil.
func NewRegisterResourceClient(conn grpc.ClientConnInterface) RegisterResourceClient {
	if conn == nil {
		return nil
	}
	return iampb.NewInternalIAMServiceClient(conn)
}

// DecodeFGARegisterIntent is the drainer.Decoder[domain.FGARegisterIntent] for
// `kacho_nlb.fga_register_outbox`.payload. Malformed JSON, an empty tuple set,
// or an incomplete tuple wraps drainer.ErrPermanent → the drainer poisons the
// row instead of retrying forever.
func DecodeFGARegisterIntent(payload []byte) (domain.FGARegisterIntent, error) {
	var i domain.FGARegisterIntent
	if err := json.Unmarshal(payload, &i); err != nil {
		return domain.FGARegisterIntent{}, fmt.Errorf("%w: fga_register_outbox: invalid json: %s", drainer.ErrPermanent, err)
	}
	if len(i.Tuples) == 0 {
		return domain.FGARegisterIntent{}, fmt.Errorf("%w: fga_register_outbox: empty tuple set", drainer.ErrPermanent)
	}
	for idx, t := range i.Tuples {
		if !t.Valid() {
			return domain.FGARegisterIntent{}, fmt.Errorf(
				"%w: fga_register_outbox: incomplete tuple[%d] (subject=%q relation=%q object=%q)",
				drainer.ErrPermanent, idx, t.SubjectID, t.Relation, t.Object)
		}
	}
	return i, nil
}

// NewRegisterApplier returns a drainer.Applier[domain.FGARegisterIntent] backed
// by the kacho-iam Internal FGA-proxy. Caller wires it into
// drainer.New[domain.FGARegisterIntent](pool, cfg, iam.DecodeFGARegisterIntent,
// iam.NewRegisterApplier(cli), logger).
//
// For each tuple in the intent it calls RegisterResource (event_type
// fga.register) or UnregisterResource (fga.unregister). The first non-OK reply
// (after error classification) short-circuits and is returned; the drainer
// retries the whole row, and IAM idempotency makes the already-applied tuples
// no-ops on replay.
func NewRegisterApplier(cli RegisterResourceClient) drainer.Applier[domain.FGARegisterIntent] {
	return func(ctx context.Context, eventType string, intent domain.FGARegisterIntent) error {
		if cli == nil {
			// No IAM peer configured — transient (drainer retries; intent stays
			// durable until the peer is wired). Never a silent success.
			return fmt.Errorf("%w: iam register client not configured", domain.ErrUnavailable)
		}
		// PropagateOutgoing so the iam-side principal/identity extractor sees the
		// real caller context. Service→service identity for
		// the fgaproxy least-priv gate comes from the mTLS client-cert.
		ctx = auth.PropagateOutgoing(ctx)

		// forward the owner labels + parent-scope + monotonic
		// source_version so kacho-iam populates its output-only resource_mirror
		// (label+parent sync feeding the γ selector). Fields are additive/optional —
		// empty values mirror gracefully; zero source_version → nil (IAM '-infinity').
		srcVer := sourceVersionPB(intent.SourceVersion)
		switch eventType {
		case domain.FGAEventRegister:
			for _, t := range intent.Tuples {
				_, err := cli.RegisterResource(ctx, &iampb.RegisterResourceRequest{
					SubjectId:       t.SubjectID,
					Relation:        t.Relation,
					Object:          t.Object,
					TraceId:         intent.ResourceID,
					Labels:          intent.Labels,
					ParentProjectId: intent.ParentProjectID,
					ParentAccountId: intent.ParentAccountID,
					SourceVersion:   srcVer,
				})
				if cerr := classifyRegisterErr(err); cerr != nil {
					return cerr
				}
			}
			return nil
		case domain.FGAEventUnregister:
			for _, t := range intent.Tuples {
				// Symmetry: Unregister removes the mirror row by object; mirror fields
				// are carried for message-shape symmetry but IAM uses only object +
				// source_version (tombstone-version: a stale tombstone won't wipe a
				// fresher row — hardening parity with compute).
				_, err := cli.UnregisterResource(ctx, &iampb.UnregisterResourceRequest{
					SubjectId:       t.SubjectID,
					Relation:        t.Relation,
					Object:          t.Object,
					TraceId:         intent.ResourceID,
					Labels:          intent.Labels,
					ParentProjectId: intent.ParentProjectID,
					ParentAccountId: intent.ParentAccountID,
					SourceVersion:   srcVer,
				})
				if cerr := classifyRegisterErr(err); cerr != nil {
					return cerr
				}
			}
			return nil
		default:
			return fmt.Errorf("%w: fga_register_outbox: unknown event_type %q", drainer.ErrPermanent, eventType)
		}
	}
}

// sourceVersionPB converts the decoded intent source_version to a proto
// Timestamp. A zero time (legacy payload / decode of an old outbox row) → nil,
// which kacho-iam treats as '-infinity' (applies unconditionally — back-compat).
func sourceVersionPB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// classifyRegisterErr maps the kacho-iam RegisterResource/UnregisterResource
// gRPC reply onto the drainer's three-way classification.
//
//	nil                              → nil (applied; or idempotent OK)
//	codes.AlreadyExists              → ErrAlreadyApplied (defensive; returns
//	                                   OK on repeat, but a stricter IAM build that
//	                                   surfaces AlreadyExists is still a success)
//	codes.InvalidArgument            → ErrPermanent (malformed tuple — retry futile)
//	codes.Unavailable / Deadline     → raw (transient — drainer retries; intent
//	                                   durable; fail-closed)
//	codes.PermissionDenied           → raw (transient: least-priv SA-relation may
//	                                   be seeded shortly after first boot; retry)
//	otherwise                        → raw (transient)
func classifyRegisterErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		// Non-status (e.g. raw transport) — transient.
		return err
	}
	switch st.Code() {
	case codes.AlreadyExists:
		return fmt.Errorf("%w: iam register reports duplicate: %s", drainer.ErrAlreadyApplied, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: iam register rejected (no retry): %s", drainer.ErrPermanent, st.Message())
	default:
		// Unavailable / DeadlineExceeded / PermissionDenied / Internal / … —
		// transient: propagate raw, drainer retries with exp backoff.
		return err
	}
}

// Compile-time guards — the returned Applier / Decoder match the drainer's
// generic signatures. A drainer signature change fails to compile here rather
// than at the wiring site in main.go.
var _ drainer.Applier[domain.FGARegisterIntent] = NewRegisterApplier(nil)
var _ drainer.Decoder[domain.FGARegisterIntent] = DecodeFGARegisterIntent
