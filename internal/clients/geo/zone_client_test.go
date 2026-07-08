// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package geo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	geopb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeZoneService — in-memory geo.ZoneServiceServer (single-page only; polling
// tests live separately if needed).
type fakeZoneService struct {
	geopb.UnimplementedZoneServiceServer

	resp *geopb.ListZonesResponse
	err  error
}

func (f *fakeZoneService) List(_ context.Context, _ *geopb.ListZonesRequest) (*geopb.ListZonesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestZoneClient_ListZoneIDsInRegion_HappyPath(t *testing.T) {
	fake := &fakeZoneService{resp: &geopb.ListZonesResponse{
		Zones: []*geopb.Zone{
			{Id: "ru-central1-a", RegionId: "ru-central1"},
			{Id: "ru-central1-b", RegionId: "ru-central1"},
			{Id: "ru-west1-a", RegionId: "ru-west1"},
		},
	}}
	conn := startFakeGeo(t, nil, fake)
	c := NewZoneClient(conn)

	got, err := c.ListZoneIDsInRegion(ctxBackground(), "ru-central1")
	require.NoError(t, err)
	assert.Equal(t, []string{"ru-central1-a", "ru-central1-b"}, got)
}

func TestZoneClient_ListZoneIDsInRegion_PermissionDenied(t *testing.T) {
	fake := &fakeZoneService{err: status.Error(codes.PermissionDenied, "scope")}
	conn := startFakeGeo(t, nil, fake)
	c := NewZoneClient(conn)
	_, err := c.ListZoneIDsInRegion(ctxBackground(), "ru-central1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestZoneClient_ListZoneIDsInRegion_Unavailable(t *testing.T) {
	fake := &fakeZoneService{err: status.Error(codes.Unavailable, "geo down")}
	conn := startFakeGeo(t, nil, fake)
	c := NewZoneClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.ListZoneIDsInRegion(ctx, "ru-central1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestZoneClient_ListZoneIDsInRegion_EmptyID(t *testing.T) {
	c := NewZoneClient(startFakeGeo(t, nil, &fakeZoneService{}))
	_, err := c.ListZoneIDsInRegion(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestZoneClient_NilConn(t *testing.T) { assert.Nil(t, NewZoneClient(nil)) }

func TestNewZoneClientFromStubs_NilStub(t *testing.T) {
	assert.Nil(t, NewZoneClientFromStubs(nil))
}

// blockingZoneService — fake ZoneServiceServer that never returns from List
// until explicitly released (simulates a hung/stalled kacho-geo peer).
type blockingZoneService struct {
	geopb.UnimplementedZoneServiceServer
	release chan struct{}
}

func (f *blockingZoneService) List(_ context.Context, _ *geopb.ListZonesRequest) (*geopb.ListZonesResponse, error) {
	<-f.release
	return &geopb.ListZonesResponse{}, nil
}

// TestZoneClient_ListZoneIDsInRegion_HangingPeer_BoundsToConfiguredTimeout —
// regression for the missing per-call deadline (round-6 audit finding 2,
// sibling client region_client.go DefaultRegionGetTimeout): a stalled
// kacho-geo peer must not park the calling goroutine forever. List is called
// with a deadline-less caller ctx (context.Background()) — the client itself
// must bound the call to ~its configured per-call timeout and fail closed
// (DeadlineExceeded -> domain.ErrUnavailable), not hang.
func TestZoneClient_ListZoneIDsInRegion_HangingPeer_BoundsToConfiguredTimeout(t *testing.T) {
	fake := &blockingZoneService{release: make(chan struct{})}
	conn := startFakeGeo(t, nil, fake)

	const configuredTimeout = 100 * time.Millisecond
	c := NewZoneClientWithTimeout(conn, configuredTimeout)

	start := time.Now()
	_, err := c.ListZoneIDsInRegion(context.Background(), "ru-central1")
	elapsed := time.Since(start)
	// Release the still-in-flight fake handler goroutine synchronously (NOT
	// via t.Cleanup: startFakeGeo's own GracefulStop cleanup runs LIFO and
	// would deadlock waiting on this still-blocked handler otherwise).
	close(fake.release)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUnavailable),
		"expected fail-closed domain.ErrUnavailable on peer hang; got %v", err)
	assert.Less(t, elapsed, 2*time.Second,
		"List must bound to the configured per-call timeout (~%s), not hang on an unresponsive peer; took %s",
		configuredTimeout, elapsed)
}
