// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import "errors"

// Sentinel-ошибки domain/service-слоя для NLB.
//
// Живут в leaf-пакете `domain`, чтобы:
//   - mock'и репо могли возвращать их без import-cycle с `internal/service`;
//   - `service.mapRepoErr` маппит SQLSTATE → один из этих
//     sentinel-ов → gRPC-code единообразно.
//
// `errors.Is` работает по identity — service- и repo-пакеты ре-экспортируют
// эти переменные через type-alias (зеркалит kacho-vpc / kacho-compute).
//
// gRPC-маппинг:
//
//	ErrNotFound            → codes.NotFound
//	ErrAlreadyExists       → codes.AlreadyExists       (UNIQUE-violation 23505)
//	ErrFailedPrecondition  → codes.FailedPrecondition  (FK 23503 / CAS-miss / status-mismatch)
//	ErrInvalidArg          → codes.InvalidArgument     (CHECK 23514 / domain Validate)
//	ErrInternal            → codes.Internal            (нераспознанная DB-ошибка; no leak)
//	ErrUnavailable         → codes.Unavailable         (peer недоступен, retryable)
var (
	ErrNotFound           = errors.New("not found")
	ErrAlreadyExists      = errors.New("already exists")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrInvalidArg         = errors.New("invalid argument")
	ErrInternal           = errors.New("internal database error")
	ErrUnavailable        = errors.New("service unavailable")
)
