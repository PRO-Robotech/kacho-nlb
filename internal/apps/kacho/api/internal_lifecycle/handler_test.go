// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// handler_test.go — pure-unit tests (no DB). Покрывают:
//
//   - helper'ы: parseEventID, mapActionToOp, extractPayloadFields;
//   - sync-валидация Subscribe (kinds whitelist, resume_from_event_id);
//   - semaphore-boundary (33-й client при cap=32 → ResourceExhausted);
//   - sync-fail без acquire слота (bad request возвращается до TryAcquire).
//
// Integration-tests с реальным Postgres + LISTEN/NOTIFY — в integration_test.go.
package internal_lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// fakeLifecycleConn — in-memory kachorepo.LifecycleConn для unit-тестов
// (без pgx/DB). Отдаёт canned-events на первом EventsSince, потом пусто;
// WaitForNotification возвращает context.Canceled (граничный «cancel» путь,
// выводящий Subscribe из wait-loop'а).
type fakeLifecycleConn struct {
	events []kachorepo.OutboxEvent
	served bool
	closed bool
}

func (c *fakeLifecycleConn) EventsSince(_ context.Context, _ int64, _ []string, _ int) ([]kachorepo.OutboxEvent, error) {
	if c.served {
		return nil, nil
	}
	c.served = true
	return c.events, nil
}

func (c *fakeLifecycleConn) WaitForNotification(_ context.Context) error { return context.Canceled }

func (c *fakeLifecycleConn) Close() { c.closed = true }

// fakeLifecycleFeed — kachorepo.LifecycleFeed: Open отдаёт conn либо openErr.
type fakeLifecycleFeed struct {
	conn    *fakeLifecycleConn
	openErr error
}

func (f *fakeLifecycleFeed) Open(_ context.Context) (kachorepo.LifecycleConn, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.conn, nil
}

// =============================================================================
// Pure helpers
// =============================================================================

func TestParseEventID(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1", 1, false},
		{"42", 42, false},
		{"9223372036854775807", 9223372036854775807, false}, // max int64
		{"-1", 0, true},
		{"abc", 0, true},
		{"1.5", 0, true},
		{" 1", 0, true}, // strconv strict
	}
	for _, c := range cases {
		got, err := parseEventID(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseEventID(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEventID(%q): unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseEventID(%q): got %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMapActionToOp(t *testing.T) {
	cases := map[string]string{
		"CREATED": "Create",
		"UPDATED": "Update",
		"DELETED": "Delete",
		"MOVED":   "Move",
		"FAILED":  "Failed",
		"UNKNOWN": "UNKNOWN", // defensive passthrough
		"":        "",
	}
	for in, want := range cases {
		if got := mapActionToOp(in); got != want {
			t.Errorf("mapActionToOp(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestExtractPayloadFields(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("empty payload", func(t *testing.T) {
		p, o := extractPayloadFields(nil, log, 1)
		if p != "" || o != "" {
			t.Fatalf("got (%q,%q), want (\"\",\"\")", p, o)
		}
	})

	t.Run("empty object", func(t *testing.T) {
		p, o := extractPayloadFields([]byte(`{}`), log, 1)
		if p != "" || o != "" {
			t.Fatalf("got (%q,%q), want (\"\",\"\")", p, o)
		}
	})

	t.Run("parent only", func(t *testing.T) {
		p, o := extractPayloadFields([]byte(`{"parent_resource_id":"nlb-123"}`), log, 1)
		if p != "nlb-123" || o != "" {
			t.Fatalf("got (%q,%q), want (\"nlb-123\",\"\")", p, o)
		}
	})

	t.Run("both fields (MOVED)", func(t *testing.T) {
		p, o := extractPayloadFields(
			[]byte(`{"parent_resource_id":"nlb-X","old_project_id":"prj-old"}`),
			log, 1)
		if p != "nlb-X" || o != "prj-old" {
			t.Fatalf("got (%q,%q), want (\"nlb-X\",\"prj-old\")", p, o)
		}
	})

	t.Run("non-string field — ignored", func(t *testing.T) {
		p, o := extractPayloadFields(
			[]byte(`{"parent_resource_id":42,"old_project_id":null}`),
			log, 1)
		if p != "" || o != "" {
			t.Fatalf("got (%q,%q), want (\"\",\"\")", p, o)
		}
	})

	t.Run("bad JSON — graceful empty + warn", func(t *testing.T) {
		p, o := extractPayloadFields([]byte(`not-json`), log, 1)
		if p != "" || o != "" {
			t.Fatalf("got (%q,%q), want (\"\",\"\")", p, o)
		}
	})

	t.Run("extra fields — ignored", func(t *testing.T) {
		p, o := extractPayloadFields(
			[]byte(`{"parent_resource_id":"X","extra":"ignored","old_project_id":"Y"}`),
			log, 1)
		if p != "X" || o != "Y" {
			t.Fatalf("got (%q,%q), want (\"X\",\"Y\")", p, o)
		}
	})
}

// =============================================================================
// Subscribe sync-validation (no DB needed; bad request fails before connect)
// =============================================================================

// fakeServerStream — минимальный grpc.ServerStream для unit-тестов.
// Реализует только Send + Context (остальные методы panic'ают —
// если handler их позовёт, это баг handler'а).
type fakeServerStream struct {
	ctx  context.Context
	sent []*lbv1.ResourceLifecycleEvent
	// sendErr — если ненулевой, Send возвращает его (для теста client-disconnect).
	sendErr error
}

func newFakeStream(ctx context.Context) *fakeServerStream {
	return &fakeServerStream{ctx: ctx}
}

func (f *fakeServerStream) Send(ev *lbv1.ResourceLifecycleEvent) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, ev)
	return nil
}

// Context — единственный другой method, который handler реально зовёт.
func (f *fakeServerStream) Context() context.Context { return f.ctx }

// SetHeader / SendHeader / SetTrailer — grpc-go ServerStream contract.
// Handler их не зовёт; реализуем как no-op для type safety.
func (f *fakeServerStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeServerStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeServerStream) SetTrailer(metadata.MD)       {}

// SendMsg / RecvMsg — handler работает через типизированный Send выше; эти
// два используются grpc-runtime для marshalling, в тестах не вызываются.
func (f *fakeServerStream) SendMsg(any) error { return nil }
func (f *fakeServerStream) RecvMsg(any) error { return nil }

// newTestHandler — handler с feed, чей Open всегда падает (sync-валидационные
// тесты до Open не доходят; connect-fail тест опирается на этот fail-on-Open).
func newTestHandler(t *testing.T, max int) *Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(&fakeLifecycleFeed{openErr: errors.New("dial refused")}, max, log)
}

// TestSubscribe_RejectsUnknownKind — sync InvalidArgument, без acquire слота.
func TestSubscribe_RejectsUnknownKind(t *testing.T) {
	h := newTestHandler(t, 1)
	stream := newFakeStream(context.Background())

	err := h.Subscribe(&lbv1.SubscribeRequest{Kinds: []string{"bogus_kind"}}, stream)
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if !strings.Contains(st.Message(), "unknown kind") {
		t.Fatalf("error msg: got %q, expected to contain 'unknown kind'", st.Message())
	}
	// Slot НЕ acquired (валидация ДО TryAcquire — иначе bad client может выпить слоты).
	if got := h.sem.Held(); got != 0 {
		t.Fatalf("slot leaked: held=%d, want 0", got)
	}
}

func TestSubscribe_RejectsBadResumeFromEventId(t *testing.T) {
	h := newTestHandler(t, 1)
	stream := newFakeStream(context.Background())

	err := h.Subscribe(&lbv1.SubscribeRequest{ResumeFromEventId: "not-a-number"}, stream)
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if h.sem.Held() != 0 {
		t.Fatalf("slot leaked: held=%d", h.sem.Held())
	}
}

func TestSubscribe_RejectsNegativeResumeFromEventId(t *testing.T) {
	h := newTestHandler(t, 1)
	stream := newFakeStream(context.Background())

	err := h.Subscribe(&lbv1.SubscribeRequest{ResumeFromEventId: "-5"}, stream)
	if err == nil {
		t.Fatal("expected InvalidArgument for negative cursor")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

// TestSubscribe_ResourceExhausted — semaphore full → 33rd client (при cap=32)
// получает ResourceExhausted мгновенно, без connect к DB.
//
// Эмулируем «все слоты заняты» прямым заполнением через TryAcquire.
func TestSubscribe_ResourceExhausted(t *testing.T) {
	h := newTestHandler(t, 32)
	// Захватить все 32 слота.
	for i := 0; i < 32; i++ {
		if !h.sem.TryAcquire() {
			t.Fatalf("pre-fill #%d failed", i+1)
		}
	}

	stream := newFakeStream(context.Background())
	err := h.Subscribe(&lbv1.SubscribeRequest{}, stream)
	if err == nil {
		t.Fatal("expected ResourceExhausted, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v (code=%v)", err, st.Code())
	}
	if !strings.Contains(st.Message(), "32") {
		t.Fatalf("error msg should mention cap (32), got %q", st.Message())
	}
}

// TestSubscribe_UnavailableOnConnectFail — feed.Open fail → Unavailable +
// освобождённый слот (никакого зависания, slot не течёт).
func TestSubscribe_UnavailableOnConnectFail(t *testing.T) {
	h := newTestHandler(t, 4) // feed с openErr
	stream := newFakeStream(context.Background())

	err := h.Subscribe(&lbv1.SubscribeRequest{}, stream)
	if err == nil {
		t.Fatal("expected Unavailable, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
	// Слот освобождён, несмотря на fail в feed.Open.
	if got := h.sem.Held(); got != 0 {
		t.Fatalf("slot leaked after open-fail: held=%d", got)
	}
}

// TestSubscribe_StreamsViaFeed — handler стримит события из инжектированного
// LifecycleFeed-порта (без pgx/DB): catchup-batch уезжает в stream, payload
// extras (parent_resource_id) извлекаются, conn закрывается. Доказывает, что
// доступ к данным идёт через порт, а не прямой pgx в app-слое.
func TestSubscribe_StreamsViaFeed(t *testing.T) {
	conn := &fakeLifecycleConn{events: []kachorepo.OutboxEvent{
		{SequenceNo: 1, ResourceType: "nlb_load_balancer", ResourceID: "nlb-A",
			ProjectID: "prj-1", Action: "CREATED", Payload: []byte(`{}`), EmittedAt: time.Now().UTC()},
		{SequenceNo: 2, ResourceType: "nlb_listener", ResourceID: "lst-A",
			ProjectID: "prj-1", Action: "CREATED",
			Payload: []byte(`{"parent_resource_id":"nlb-A"}`), EmittedAt: time.Now().UTC()},
	}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(&fakeLifecycleFeed{conn: conn}, 4, log)

	stream := newFakeStream(context.Background())
	if err := h.Subscribe(&lbv1.SubscribeRequest{}, stream); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if len(stream.sent) != 2 {
		t.Fatalf("streamed %d events, want 2", len(stream.sent))
	}
	if stream.sent[0].GetResourceId() != "nlb-A" || stream.sent[0].GetOp() != "Create" {
		t.Fatalf("event[0] = %+v", stream.sent[0])
	}
	if stream.sent[1].GetParentResourceId() != "nlb-A" {
		t.Fatalf("event[1] parent = %q, want nlb-A", stream.sent[1].GetParentResourceId())
	}
	if !conn.closed {
		t.Fatal("conn.Close() not called (session leak)")
	}
	if h.sem.Held() != 0 {
		t.Fatalf("slot leaked: held=%d", h.sem.Held())
	}
}

// TestNewHandler_PanicsOnZeroMaxStreams — composition root обязан провалидировать
// max-streams > 0; handler — последний backstop.
func TestNewHandler_PanicsOnZeroMaxStreams(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on NewHandler(_, 0, _)")
		}
	}()
	_ = NewHandler(&fakeLifecycleFeed{}, 0, nil)
}
