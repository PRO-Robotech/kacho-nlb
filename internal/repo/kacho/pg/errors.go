package pg

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Ensure pgconn import не считается unused — pgErr typed-assertion использует
// его type identity напрямую в mapPgErr.
var _ *pgconn.PgError = nil

// mapPgErr классифицирует pgx-ошибку и возвращает sentinel из пакета `kacho`.
// service-слой потом мапит её на gRPC-status (см. domain/errors.go).
//
// Не leak'ает raw PG-сообщение клиенту: для неизвестных классов возвращает
// ErrInternal без exposing.
//
// kind/id — для AlreadyExists/NotFound сообщений. Skill workspace CLAUDE.md
// «Within-service refs» — все DB-violations сводятся к одному из 5 sentinel'ов.
//
// SQLSTATE table (Postgres):
//
//	23505 unique_violation             → ErrAlreadyExists
//	23503 foreign_key_violation        → ErrFailedPrecondition
//	23514 check_violation              → ErrInvalidArg
//	23P01 exclusion_violation          → ErrFailedPrecondition
//	22P02 invalid_text_representation  → ErrInvalidArg (malformed cast)
//
// pgx.ErrNoRows → ErrNotFound. Все остальное → ErrInternal.
func mapPgErr(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if id != "" {
			return fmt.Errorf("%w: %s %s not found", kacho.ErrNotFound, kind, id)
		}
		return kacho.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s with name already exists", kacho.ErrAlreadyExists, kind)
		case "23503":
			return fmt.Errorf("%w: %s has dependent resources", kacho.ErrFailedPrecondition, kind)
		case "23514":
			return fmt.Errorf("%w: %s violates check constraint", kacho.ErrInvalidArg, kind)
		case "23P01":
			return fmt.Errorf("%w: %s value conflicts", kacho.ErrFailedPrecondition, kind)
		case "22P02":
			return fmt.Errorf("%w: invalid %s id '%s'", kacho.ErrInvalidArg, strings.ToLower(kind), id)
		}
	}
	return fmt.Errorf("%w: %v", kacho.ErrInternal, err)
}

// invalidArg формирует kacho.ErrInvalidArg с user-friendly текстом —
// используется для page-token decode errors и т.п.
func invalidArg(field, msg string) error {
	return fmt.Errorf("%w: %s: %s", kacho.ErrInvalidArg, field, msg)
}

// pageCursor — opaque payload для PageToken: (created_at, id) snapshot.
type pageCursor struct {
	CreatedAt time.Time
	ID        string
}

// encodePageToken — base64-encoded "RFC3339Nano\x00id". Skill workspace CLAUDE.md
// — opaque cursor: не показываем внутренности клиенту.
func encodePageToken(t time.Time, id string) string {
	if t.IsZero() && id == "" {
		return ""
	}
	raw := t.UTC().Format(time.RFC3339Nano) + "\x00" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodePageToken — обратное преобразование. Malformed token →
// invalidArg("page_token", ...) (ErrInvalidArg → gRPC InvalidArgument).
func decodePageToken(token string) (pageCursor, error) {
	if token == "" {
		return pageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return pageCursor{}, invalidArg("page_token", "malformed base64")
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 {
		return pageCursor{}, invalidArg("page_token", "malformed payload")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return pageCursor{}, invalidArg("page_token", "malformed timestamp")
	}
	return pageCursor{CreatedAt: t, ID: parts[1]}, nil
}

// pageSizeOrDefault — clamp page_size в [1, MaxPageSize]; 0 → DefaultPageSize=50.
func pageSizeOrDefault(p int64) (int64, error) {
	const (
		defaultPageSize = 50
		maxPageSize     = 1000
	)
	if p == 0 {
		return defaultPageSize, nil
	}
	if p < 0 || p > maxPageSize {
		return 0, coreerrors.InvalidArgument().
			AddFieldViolation("page_size",
				fmt.Sprintf("page_size must be in range [1, %d]", maxPageSize)).
			Err()
	}
	return p, nil
}
