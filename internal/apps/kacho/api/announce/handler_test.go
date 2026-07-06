// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package announce_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/announce"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// fakeStore — ручной двойник announce.Store (port). Фиксирует вызовы и отдаёт
// заданный результат — use-case тестируется в изоляции от pgx.
type fakeStore struct {
	loadRec     *kachorepo.AnnounceStateRecord
	loadFound   bool
	loadErr     error
	reportErr   error
	reportCalls int
	lastLBID    string
	lastZones   []domain.AnnounceZone
}

func (f *fakeStore) ReportZones(_ context.Context, lbID string, zones []domain.AnnounceZone) error {
	f.reportCalls++
	f.lastLBID = lbID
	f.lastZones = zones
	return f.reportErr
}

func (f *fakeStore) LoadState(_ context.Context, lbID string) (*kachorepo.AnnounceStateRecord, bool, error) {
	f.lastLBID = lbID
	return f.loadRec, f.loadFound, f.loadErr
}

func newAnnounceHandler(s announce.Store) *announce.Handler {
	return announce.NewHandler(s, nil)
}

func freshLBID(t *testing.T) string {
	t.Helper()
	return ids.NewID(ids.PrefixLoadBalancer)
}

func TestGetAnnounceState_Happy_PerZone(t *testing.T) {
	id := freshLBID(t)
	now := time.Now().UTC().Truncate(time.Second)
	store := &fakeStore{
		loadFound: true,
		loadRec: &kachorepo.AnnounceStateRecord{
			LoadBalancerID: id,
			AddressV4:      "203.0.113.10",
			AddressV6:      "2001:db8::10",
			ObservedAt:     now,
			Zones: []kachorepo.AnnounceZoneRecord{
				{
					AnnounceZone: domain.AnnounceZone{
						ZoneID: "zone-a", IPVersion: domain.IPVersionV4,
						BGPSessionUp: true, RouteID: "rt-1", VrfID: "vrf-1",
						KernelProgrammed: true, InfraID: 42,
					},
					UpdatedAt: now,
				},
				{
					AnnounceZone: domain.AnnounceZone{
						ZoneID: "zone-b", IPVersion: domain.IPVersionV6,
						BGPSessionUp: false, RouteID: "rt-2", VrfID: "vrf-2",
						KernelProgrammed: false, InfraID: 0,
					},
					UpdatedAt: now,
				},
			},
		},
	}
	h := newAnnounceHandler(store)

	resp, err := h.GetAnnounceState(context.Background(),
		&lbv1.GetLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: id})
	require.NoError(t, err)
	require.Equal(t, id, resp.GetNetworkLoadBalancerId())
	require.Equal(t, "203.0.113.10", resp.GetAddressV4())
	require.Equal(t, "2001:db8::10", resp.GetAddressV6())
	require.Len(t, resp.GetZones(), 2)

	z0 := resp.GetZones()[0]
	require.Equal(t, "zone-a", z0.GetZoneId())
	require.Equal(t, lbv1.IpVersion_IPV4, z0.GetIpVersion())
	require.True(t, z0.GetBgpUp())
	require.Equal(t, "rt-1", z0.GetRouteId())
	require.Equal(t, "vrf-1", z0.GetVrfId())
	require.True(t, z0.GetKernelProgrammed())
	require.Equal(t, int64(42), z0.GetInfraId())

	z1 := resp.GetZones()[1]
	require.Equal(t, lbv1.IpVersion_IPV6, z1.GetIpVersion())
	require.False(t, z1.GetBgpUp())
}

func TestGetAnnounceState_NotFound(t *testing.T) {
	id := freshLBID(t)
	h := newAnnounceHandler(&fakeStore{loadFound: false})
	_, err := h.GetAnnounceState(context.Background(),
		&lbv1.GetLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: id})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetAnnounceState_EmptyID_InvalidArgument(t *testing.T) {
	h := newAnnounceHandler(&fakeStore{})
	_, err := h.GetAnnounceState(context.Background(),
		&lbv1.GetLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: ""})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetAnnounceState_MalformedID_InvalidArgument(t *testing.T) {
	h := newAnnounceHandler(&fakeStore{})
	_, err := h.GetAnnounceState(context.Background(),
		&lbv1.GetLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: "not-an-id"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestReportAnnounceState_Upsert_Idempotent(t *testing.T) {
	id := freshLBID(t)
	store := &fakeStore{}
	h := newAnnounceHandler(store)

	req := &lbv1.ReportLoadBalancerAnnounceStateRequest{
		NetworkLoadBalancerId: id,
		Zones: []*lbv1.AnnounceZoneState{
			{
				ZoneId: "zone-a", IpVersion: lbv1.IpVersion_IPV4,
				BgpUp: true, RouteId: "rt-1", VrfId: "vrf-1",
				KernelProgrammed: true, InfraId: 7,
			},
		},
	}

	resp, err := h.ReportAnnounceState(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 1, store.reportCalls)
	require.Equal(t, id, store.lastLBID)
	require.Len(t, store.lastZones, 1)
	require.Equal(t, "zone-a", store.lastZones[0].ZoneID)
	require.Equal(t, domain.IPVersionV4, store.lastZones[0].IPVersion)
	require.True(t, store.lastZones[0].BGPSessionUp)
	require.Equal(t, int64(7), store.lastZones[0].InfraID)

	// Повторный репорт того же набора — идемпотентен (повторно проходит, store
	// делает upsert ещё раз без ошибки).
	resp2, err2 := h.ReportAnnounceState(context.Background(), req)
	require.NoError(t, err2)
	require.NotNil(t, resp2)
	require.Equal(t, 2, store.reportCalls)
}

// TestGetAnnounceState_UnknownCodeNormalizedToInternal — a store error that is a
// gRPC status carrying codes.Unknown must NOT be forwarded as-is; it must be
// normalized to Internal (no-leak), matching shared.MapDomainErr used by the
// loadbalancer/listener/targetgroup handlers. A per-package
// pass-through that forwards any status (incl. Unknown) is the divergence the
// shared mapper was created to eliminate.
func TestGetAnnounceState_UnknownCodeNormalizedToInternal(t *testing.T) {
	id := freshLBID(t)
	store := &fakeStore{loadErr: status.Error(codes.Unknown, "opaque backend boom")}
	h := newAnnounceHandler(store)
	_, err := h.GetAnnounceState(context.Background(),
		&lbv1.GetLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: id})
	require.Equal(t, codes.Internal, status.Code(err),
		"Unknown-coded store error must be normalized to Internal, not forwarded verbatim")
}

// TestReportAnnounceState_FailedPreconditionMapped — a domain FailedPrecondition
// sentinel (e.g. FK to a missing LB) maps to gRPC FailedPrecondition.
func TestReportAnnounceState_FailedPreconditionMapped(t *testing.T) {
	id := freshLBID(t)
	store := &fakeStore{reportErr: domain.ErrFailedPrecondition}
	h := newAnnounceHandler(store)
	_, err := h.ReportAnnounceState(context.Background(),
		&lbv1.ReportLoadBalancerAnnounceStateRequest{
			NetworkLoadBalancerId: id,
			Zones:                 []*lbv1.AnnounceZoneState{{ZoneId: "zone-a"}},
		})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestReportAnnounceState_EmptyID_InvalidArgument(t *testing.T) {
	h := newAnnounceHandler(&fakeStore{})
	_, err := h.ReportAnnounceState(context.Background(),
		&lbv1.ReportLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: ""})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestReportAnnounceState_ZoneMissingZoneID_InvalidArgument(t *testing.T) {
	id := freshLBID(t)
	store := &fakeStore{}
	h := newAnnounceHandler(store)
	_, err := h.ReportAnnounceState(context.Background(),
		&lbv1.ReportLoadBalancerAnnounceStateRequest{
			NetworkLoadBalancerId: id,
			Zones:                 []*lbv1.AnnounceZoneState{{ZoneId: ""}},
		})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, 0, store.reportCalls)
}
