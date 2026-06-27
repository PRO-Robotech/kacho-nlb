// Package internal_lifecycle — Internal endpoint InternalResourceLifecycleService
// (server-stream Subscribe для D-13 — потребитель kacho-iam, не наружу).
//
// Подписка iam на lifecycle-events ресурсов nlb_load_balancer / nlb_listener /
// nlb_target_group (CREATED / UPDATED / DELETED / MOVED / FAILED) для синхронизации
// FGA tuples. Слушает `nlb_outbox` LISTEN/NOTIFY на dedicated pgx.Conn (вне pgxpool —
// LISTEN не работает корректно на pooled conn).
//
// Internal endpoint — порт 9091, НЕ маршрутизируется через api-gateway external
// TLS endpoint (workspace §запрет 6).
//
// Алгоритм Subscribe (см. design §3.6 / §4.8 + acceptance §9 GWT-XRES-004..006):
//  1. TryAcquire слот semaphore (cap = MaxStreams); fail → ResourceExhausted.
//  2. pgx.Connect(ctx, dsn) под inner timeout — dedicated conn.
//  3. LISTEN nlb_outbox.
//  4. Initial catchup: SELECT * FROM nlb_outbox WHERE sequence_no > $cursor
//     [AND resource_type = ANY($kinds)] ORDER BY sequence_no ASC LIMIT 100;
//     loop пока batch < 100.
//  5. Wait loop: conn.WaitForNotification(ctx, 30s timeout) → repoll новые
//     события из nlb_outbox → stream.Send(*lbv1.ResourceLifecycleEvent{...}).
//  6. defer: UNLISTEN + conn.Close + semaphore.Release.
//
// Pattern скопирован с kacho-vpc/internal/handler/internal_watch_handler.go
// (та же LISTEN/NOTIFY-семантика), адаптирован под loadbalancer-specific
// proto-сообщения и схему nlb_outbox.
package internal_lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// catchupBatchSize — сколько событий читаем за один SELECT при initial-catchup.
// Batch < этого числа = end-of-data; выходим из catchup-loop.
const catchupBatchSize = 100

// listenChannel — Postgres NOTIFY channel, на который шлёт trigger
// `nlb_outbox_notify_trg` после INSERT в `nlb_outbox` (см. миграцию 0001).
const listenChannel = "nlb_outbox"

// pollIdleTimeout — periodic re-poll на случай missed NOTIFY (например при
// listener pause из-за GC). Bounded resource usage.
const pollIdleTimeout = 30 * time.Second

// connectTimeout — защита от self-DoS если Postgres перегружен. Слот
// semaphore удерживается ровно столько; иначе медленный Connect размазал
// бы все слоты на 5+ секунд под нагрузкой → клиенты получали бы
// ResourceExhausted всё это время.
const connectTimeout = 2 * time.Second

// allowedKinds — whitelist resource_type-значений из CHECK-constraint
// nlb_outbox (миграция 0001). Используется для валидации
// SubscribeRequest.kinds: неизвестный kind → InvalidArgument.
var allowedKinds = map[string]struct{}{
	"nlb_load_balancer": {},
	"nlb_listener":      {},
	"nlb_target_group":  {},
}

// Handler реализует lbv1.InternalResourceLifecycleServiceServer.
//
// Зависимости:
//   - dsn — connection string для **dedicated** pgx.Conn (LISTEN не работает
//     на pooled). Берётся из cfg.Repository.Postgres.URL composition-root'а.
//   - log — structured logger (slog), для diagnostics. nil-safe (см. NewHandler).
//   - sem — semaphore, ограничивает concurrent streams (cap = MaxStreams).
type Handler struct {
	lbv1.UnimplementedInternalResourceLifecycleServiceServer

	dsn string
	log *slog.Logger
	sem *Semaphore
}

// NewHandler создаёт handler с заданными параметрами.
//
//   - dsn — connection string для dedicated LISTEN-conn'ов. Required (пустой
//     приведёт к Unavailable на первом Subscribe).
//   - maxStreams — cap concurrent streams. Должен быть > 0; caller (composition
//     root) обязан провалидировать. Здесь мы panic'аем на <=0 как safety-net.
//   - log — structured logger; nil → discard via slog.Default (всё равно
//     записывает, но в default destination).
func NewHandler(dsn string, maxStreams int, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		dsn: dsn,
		log: log,
		sem: NewSemaphore(maxStreams),
	}
}

// Subscribe — server-stream RPC. См. package-doc для полного алгоритма.
//
// Граничные случаи:
//   - client cancel: ctx стрима cancelled → возвращаем nil (graceful).
//   - LISTEN-conn drop: Unavailable, клиент делает retry.
//   - DB transient на catchup-SELECT: Internal без leak'а pgx-сообщения.
//   - Все слоты semaphore заняты: ResourceExhausted (быстрый fail, не блок).
//   - Неизвестный kind в request: InvalidArgument (sync, до acquire слота).
//   - Невалидный resume_from_event_id: InvalidArgument (sync).
func (h *Handler) Subscribe(
	req *lbv1.SubscribeRequest,
	stream lbv1.InternalResourceLifecycleService_SubscribeServer,
) error {
	ctx := stream.Context()

	// 0. Sync-валидация request: kinds whitelist + resume_from_event_id format.
	// Делается ДО acquire слота — не хотим тратить слот на bad request.
	kinds := req.GetKinds()
	for _, k := range kinds {
		if _, ok := allowedKinds[k]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"unknown kind %q (allowed: nlb_load_balancer, nlb_listener, nlb_target_group)", k)
		}
	}
	cursor, err := parseEventID(req.GetResumeFromEventId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument,
			"invalid resume_from_event_id %q: must be decimal sequence_no", req.GetResumeFromEventId())
	}

	// 1. Acquire stream slot — non-blocking. Если все слоты заняты, отвечаем
	// ResourceExhausted сразу, не задерживаем клиента. Защита от DoS: один
	// buggy client (или iam с тысячью stale-tabs) не может выпить connection
	// pool под LISTEN.
	if !h.sem.TryAcquire() {
		return status.Errorf(codes.ResourceExhausted,
			"max concurrent lifecycle streams reached (%d); retry later", h.sem.Capacity())
	}
	defer h.sem.Release()

	h.log.Info("lifecycle stream started",
		"resume_from_event_id", cursor,
		"kinds", kinds,
		"slots_held", h.sem.Held(),
		"slots_cap", h.sem.Capacity(),
	)

	// 2. Dedicated pgx.Conn вне пула — гарантированная изоляция LISTEN-сессии.
	// При abnormal exit (panic, server kill) Close() из defer не выполнится, но
	// TCP-conn закроется → pool не затронут.
	connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
	conn, err := pgx.Connect(connectCtx, h.dsn)
	connectCancel()
	if err != nil {
		// Generic Unavailable без leak'а pgx-text (db hostname / port / sslmode).
		h.log.Warn("lifecycle stream connect failed", "err", err)
		return status.Error(codes.Unavailable, "lifecycle backend unavailable")
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.Close(closeCtx)
		cancelClose()
	}()

	// 3. LISTEN на trigger-канал. Идентификатор — literal, НЕ из user-input
	// (защита от SQL-injection через user-controllable LISTEN-channel).
	if _, err := conn.Exec(ctx, "LISTEN "+listenChannel); err != nil {
		h.log.Warn("lifecycle stream LISTEN failed", "err", err)
		return status.Error(codes.Unavailable, "lifecycle backend unavailable")
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = conn.Exec(closeCtx, "UNLISTEN "+listenChannel)
		cancelClose()
	}()

	// 4. Initial catchup: вычерпать все события начиная с cursor.
	cursor, err = h.streamSince(ctx, conn, cursor, kinds, stream)
	if err != nil {
		return err
	}

	// 5. Wait loop: NOTIFY / timeout / ctx.Done.
	for {
		if err := ctx.Err(); err != nil {
			h.log.Info("lifecycle stream cancelled", "err", err)
			return nil
		}
		waitCtx, cancel := context.WithTimeout(ctx, pollIdleTimeout)
		_, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				// Outer ctx cancelled — graceful exit.
				return nil
			case errors.Is(err, context.DeadlineExceeded):
				// Periodic re-poll timeout — продолжаем (но если outer ctx тоже
				// cancelled, вернёмся через ctx.Err()-check в начале loop'а).
				if ctx.Err() != nil {
					return nil
				}
			default:
				// pg_notify connection drop / другое. Без leak'а pgx-detail —
				// клиент видит generic Unavailable.
				h.log.Warn("lifecycle stream notification lost", "err", err)
				return status.Error(codes.Unavailable, "lifecycle notification stream lost")
			}
		}

		// 6. Re-read since cursor (несколько NOTIFY могут coalesce — читаем
		// все пропущенные events).
		cursor, err = h.streamSince(ctx, conn, cursor, kinds, stream)
		if err != nil {
			return err
		}
	}
}

// streamSince читает все события из nlb_outbox с sequence_no > cursor (и
// resource_type ∈ kinds, если задан) и шлёт их в stream. Возвращает новый
// cursor (= sequence_no последнего отправленного события).
//
// Делает несколько SELECT-ов по batchSize до тех пор, пока не вычерпаем все
// события (batch < batchSize ⇒ end-of-data).
func (h *Handler) streamSince(
	ctx context.Context,
	conn *pgx.Conn,
	cursor int64,
	kinds []string,
	stream lbv1.InternalResourceLifecycleService_SubscribeServer,
) (int64, error) {
	for {
		args := []any{cursor}
		var kindFilter string
		if len(kinds) > 0 {
			kindFilter = " AND resource_type = ANY($2)"
			args = append(args, kinds)
		}
		// LIMIT — literal int, не из request (защита от int-injection).
		q := fmt.Sprintf(`
			SELECT sequence_no, resource_type, resource_id, project_id, action, payload, emitted_at
			FROM kacho_nlb.nlb_outbox
			WHERE sequence_no > $1%s
			ORDER BY sequence_no ASC
			LIMIT %d
		`, kindFilter, catchupBatchSize)

		rows, err := conn.Query(ctx, q, args...)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return cursor, nil
			}
			h.log.Warn("lifecycle stream query failed", "err", err)
			return cursor, status.Error(codes.Internal, "lifecycle query failed")
		}

		count := 0
		for rows.Next() {
			var seq int64
			var resType, resID, projectID, action string
			var payloadJSON []byte
			var emittedAt time.Time
			if err := rows.Scan(&seq, &resType, &resID, &projectID, &action, &payloadJSON, &emittedAt); err != nil {
				rows.Close()
				h.log.Warn("lifecycle stream scan failed", "err", err)
				return cursor, status.Error(codes.Internal, "lifecycle scan failed")
			}

			parent, oldProject := extractPayloadFields(payloadJSON, h.log, seq)

			ev := &lbv1.ResourceLifecycleEvent{
				EventId:          strconv.FormatInt(seq, 10),
				Kind:             resType,
				ResourceId:       resID,
				Op:               mapActionToOp(action),
				ProjectId:        projectID,
				ParentResourceId: parent,
				OldProjectId:     oldProject,
				CreatedAt:        timestamppb.New(emittedAt),
			}
			if err := stream.Send(ev); err != nil {
				rows.Close()
				// Send-err = client gone; не мапим в gRPC code (grpc-go сам).
				return cursor, err
			}
			cursor = seq
			count++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			h.log.Warn("lifecycle stream rows iter failed", "err", err)
			return cursor, status.Error(codes.Internal, "lifecycle iter failed")
		}

		if count < catchupBatchSize {
			return cursor, nil
		}
		// full batch — продолжаем читать (могут быть ещё события).
	}
}

// mapActionToOp — outbox.action (CREATED/UPDATED/DELETED/MOVED/FAILED) →
// proto.op (Create/Update/Delete/Move/Failed).
//
// kacho-iam D-13 consumer ожидает мнемонику в proto-style (Create/Delete/Move),
// а в outbox для совместимости с другими сервисами хранится upper-case
// participle (CREATED/DELETED/MOVED). Mapping:
//
//	CREATED → Create
//	UPDATED → Update
//	DELETED → Delete
//	MOVED   → Move
//	FAILED  → Failed
//
// Неизвестное значение пробрасывается as-is (CHECK-constraint nlb_outbox
// защищает от значений вне whitelist, но defensive: лучше отдать unknown
// op, чем drop'нуть event).
func mapActionToOp(action string) string {
	switch action {
	case "CREATED":
		return "Create"
	case "UPDATED":
		return "Update"
	case "DELETED":
		return "Delete"
	case "MOVED":
		return "Move"
	case "FAILED":
		return "Failed"
	default:
		return action
	}
}

// parseEventID — `event_id` в proto-сообщении это строка-decimal sequence_no.
// Пустая строка → cursor 0 (с самого начала). Невалидная строка → ошибка
// (handler мапит в InvalidArgument).
func parseEventID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("negative")
	}
	return v, nil
}

// extractPayloadFields — извлекает parent_resource_id и old_project_id из
// JSON-payload outbox-row. Оба поля опциональны (пустые если отсутствуют).
//
// Контракт payload (см. design §3.6 / §5.4):
//   - parent_resource_id — для Listener / TargetGroup ссылается на родительский
//     NLB / NLB соответственно. Поле "parent_resource_id" в payload JSON.
//   - old_project_id — заполняется ТОЛЬКО для MOVED-event'ов (resource
//     перемещён из одного project в другой). Поле "old_project_id" в payload.
//
// Если payload — невалидный JSON или поле другого типа, логируем warn и
// возвращаем пустые значения (graceful degradation — event всё равно
// должен дойти до подписчика, лучше с пустыми extras чем drop).
func extractPayloadFields(payloadJSON []byte, log *slog.Logger, seq int64) (parent, oldProject string) {
	if len(payloadJSON) == 0 {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(payloadJSON, &m); err != nil {
		log.Warn("lifecycle: bad payload JSON", "sequence_no", seq, "err", err)
		return "", ""
	}
	if v, ok := m["parent_resource_id"].(string); ok {
		parent = v
	}
	if v, ok := m["old_project_id"].(string); ok {
		oldProject = v
	}
	return parent, oldProject
}
