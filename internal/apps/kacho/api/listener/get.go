// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// GetUseCase — sync read одного Listener'а.
//
// Использует `RepoFactory.Reader(ctx)` — read-only TX (на slave-pool если
// настроен). FGA per-resource Check выполняется до этого через interceptor
// в api-gateway; UseCase сам Check не зовёт.
type GetUseCase struct {
	repo RepoFactory
}

// NewGetUseCase — конструктор.
func NewGetUseCase(repo RepoFactory) *GetUseCase {
	return &GetUseCase{repo: repo}
}

// Run выполняет Get.
//
// Mapping:
//
//	req.ListenerId == "" → InvalidArgument "listener_id required"
//	repo.ErrNotFound     → NotFound        "Listener <id> not found"  (по конвенции Kachō)
//	other repo err       → mapDomainErr (sentinel-aware)
func (u *GetUseCase) Run(ctx context.Context, req *lbv1.GetListenerRequest) (*lbv1.Listener, error) {
	id := req.GetListenerId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "listener_id required")
	}
	if err := validateListenerID(id); err != nil {
		return nil, err
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	rec, err := rd.Listeners().Get(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return listenerRecordToPb(rec)
}
