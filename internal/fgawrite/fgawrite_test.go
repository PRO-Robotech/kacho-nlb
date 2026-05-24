package fgawrite

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// ---- Test doubles ----

type recordingWriter struct {
	creatorCalls    []creatorCall
	rewriteCalls    []rewriteCall
	writeCreatorErr error
	rewriteErr      error
}

type creatorCall struct {
	Subject  string
	Relation string
	Object   string
}

type rewriteCall struct {
	ObjectType string
	ObjectID   string
	Src        string
	Dst        string
}

func (w *recordingWriter) WriteCreatorTuple(_ context.Context, subjectID, relation, object string) error {
	w.creatorCalls = append(w.creatorCalls, creatorCall{
		Subject: subjectID, Relation: relation, Object: object,
	})
	return w.writeCreatorErr
}

func (w *recordingWriter) RewriteProjectTuple(_ context.Context, objectType, objectID, src, dst string) error {
	w.rewriteCalls = append(w.rewriteCalls, rewriteCall{
		ObjectType: objectType, ObjectID: objectID, Src: src, Dst: dst,
	})
	return w.rewriteErr
}

// ---- ObjectType / Relation constants ----

func TestObjectTypeConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, "nlb_load_balancer", ObjectTypeLoadBalancer)
	require.Equal(t, "nlb_listener", ObjectTypeListener)
	require.Equal(t, "nlb_target_group", ObjectTypeTargetGroup)
}

func TestRelationConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, "owner", RelationOwner)
	require.Equal(t, "project", RelationProject)
	require.Equal(t, "load_balancer", RelationLoadBalancer)
}

// ---- SubjectFromPrincipal ----

func TestSubjectFromPrincipal_User(t *testing.T) {
	t.Parallel()
	require.Equal(t, "user:usr-1",
		SubjectFromPrincipal(operations.Principal{Type: "user", ID: "usr-1"}))
}

func TestSubjectFromPrincipal_ServiceAccount(t *testing.T) {
	t.Parallel()
	require.Equal(t, "service_account:sa-1",
		SubjectFromPrincipal(operations.Principal{Type: "service_account", ID: "sa-1"}))
}

func TestSubjectFromPrincipal_System_Empty(t *testing.T) {
	t.Parallel()
	require.Empty(t, SubjectFromPrincipal(operations.Principal{Type: "system", ID: "bootstrap"}))
}

func TestSubjectFromPrincipal_EmptyType_Empty(t *testing.T) {
	t.Parallel()
	require.Empty(t, SubjectFromPrincipal(operations.Principal{ID: "x"}))
}

func TestSubjectFromPrincipal_EmptyID_Empty(t *testing.T) {
	t.Parallel()
	require.Empty(t, SubjectFromPrincipal(operations.Principal{Type: "user"}))
}

// ---- EmitCreator ----

func TestEmitCreator_NilWriter_NoOp(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		EmitCreator(context.Background(), nil, nil, "user:u-1", RelationOwner, ObjectTypeLoadBalancer, "nlb-x")
	})
}

func TestEmitCreator_EmptySubject_Skipped(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitCreator(context.Background(), w, nil, "", RelationOwner, ObjectTypeLoadBalancer, "nlb-x")
	require.Empty(t, w.creatorCalls)
}

func TestEmitCreator_EmptyObjectID_Skipped(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitCreator(context.Background(), w, nil, "user:u-1", RelationOwner, ObjectTypeLoadBalancer, "")
	require.Empty(t, w.creatorCalls)
}

func TestEmitCreator_Delegates(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitCreator(context.Background(), w, nil, "user:u-1", RelationOwner, ObjectTypeLoadBalancer, "nlb-x")
	require.Equal(t, []creatorCall{{Subject: "user:u-1", Relation: RelationOwner, Object: "nlb_load_balancer:nlb-x"}}, w.creatorCalls)
}

func TestEmitCreator_Error_NonFatal(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{writeCreatorErr: errors.New("openfga down")}
	require.NotPanics(t, func() {
		EmitCreator(context.Background(), w, nil, "user:u-1", RelationOwner, ObjectTypeTargetGroup, "tgr-x")
	})
	require.Len(t, w.creatorCalls, 1)
}

// ---- EmitParentLink ----

func TestEmitParentLink_NilWriter_NoOp(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		EmitParentLink(context.Background(), nil, nil,
			ObjectTypeLoadBalancer, "nlb-x", RelationLoadBalancer, ObjectTypeListener, "lst-y")
	})
}

func TestEmitParentLink_Delegates(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitParentLink(context.Background(), w, nil,
		ObjectTypeLoadBalancer, "nlb-x", RelationLoadBalancer, ObjectTypeListener, "lst-y")
	require.Equal(t, []creatorCall{{
		Subject:  "nlb_load_balancer:nlb-x",
		Relation: RelationLoadBalancer,
		Object:   "nlb_listener:lst-y",
	}}, w.creatorCalls)
}

func TestEmitParentLink_EmptyParentID_Skipped(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitParentLink(context.Background(), w, nil,
		ObjectTypeLoadBalancer, "", RelationLoadBalancer, ObjectTypeListener, "lst-y")
	require.Empty(t, w.creatorCalls)
}

func TestEmitParentLink_EmptyChildID_Skipped(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitParentLink(context.Background(), w, nil,
		ObjectTypeLoadBalancer, "nlb-x", RelationLoadBalancer, ObjectTypeListener, "")
	require.Empty(t, w.creatorCalls)
}

func TestEmitParentLink_Error_NonFatal(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{writeCreatorErr: errors.New("openfga down")}
	require.NotPanics(t, func() {
		EmitParentLink(context.Background(), w, nil,
			ObjectTypeLoadBalancer, "nlb-x", RelationLoadBalancer, ObjectTypeListener, "lst-y")
	})
	require.Len(t, w.creatorCalls, 1)
}

// ---- EmitProjectRewrite ----

func TestEmitProjectRewrite_NilWriter_NoOp(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		EmitProjectRewrite(context.Background(), nil, nil,
			ObjectTypeLoadBalancer, "nlb-x", "", "prj-1")
	})
}

func TestEmitProjectRewrite_Delegates(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitProjectRewrite(context.Background(), w, nil,
		ObjectTypeTargetGroup, "tgr-x", "prj-old", "prj-new")
	require.Equal(t, []rewriteCall{{
		ObjectType: ObjectTypeTargetGroup,
		ObjectID:   "tgr-x",
		Src:        "prj-old",
		Dst:        "prj-new",
	}}, w.rewriteCalls)
}

func TestEmitProjectRewrite_EmptyObjectID_Skipped(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitProjectRewrite(context.Background(), w, nil,
		ObjectTypeLoadBalancer, "", "", "prj-1")
	require.Empty(t, w.rewriteCalls)
}

func TestEmitProjectRewrite_EmptyDstProject_Skipped(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	EmitProjectRewrite(context.Background(), w, nil,
		ObjectTypeLoadBalancer, "nlb-x", "", "")
	require.Empty(t, w.rewriteCalls)
}

func TestEmitProjectRewrite_Error_NonFatal(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{rewriteErr: errors.New("openfga down")}
	require.NotPanics(t, func() {
		EmitProjectRewrite(context.Background(), w, nil,
			ObjectTypeListener, "lst-x", "", "prj-1")
	})
	require.Len(t, w.rewriteCalls, 1)
}
