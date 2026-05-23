package compute

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computepb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeRegionService — in-memory RegionServiceServer.
type fakeRegionService struct {
	computepb.UnimplementedRegionServiceServer

	resp *computepb.Region
	err  error
}

func (f *fakeRegionService) Get(_ context.Context, _ *computepb.GetRegionRequest) (*computepb.Region, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// fakeZoneService — возвращает фиксированный набор зон.
type fakeZoneService struct {
	computepb.UnimplementedZoneServiceServer

	zones []*computepb.Zone
	err   error
}

func (f *fakeZoneService) List(_ context.Context, _ *computepb.ListZonesRequest) (*computepb.ListZonesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &computepb.ListZonesResponse{Zones: f.zones}, nil
}

func TestRegionClient_Get_HappyPath(t *testing.T) {
	regions := &fakeRegionService{resp: &computepb.Region{Id: "ru-central1", Name: "Central Russia"}}
	zones := &fakeZoneService{zones: []*computepb.Zone{
		{Id: "ru-central1-a", RegionId: "ru-central1"},
		{Id: "ru-central1-b", RegionId: "ru-central1"},
		{Id: "us-east-1-a", RegionId: "us-east-1"}, // filter этот
	}}
	conn := startFakeCompute(t, regions, zones, nil)
	c := NewRegionClient(conn)
	require.NotNil(t, c)

	got, err := c.Get(ctxBackground(), "ru-central1")
	require.NoError(t, err)
	assert.Equal(t, "ru-central1", got.ID)
	assert.Equal(t, "Central Russia", got.Name)
	assert.ElementsMatch(t, []string{"ru-central1-a", "ru-central1-b"}, got.Zones)
}

func TestRegionClient_Get_NotFoundMapsToInvalidArg(t *testing.T) {
	regions := &fakeRegionService{err: status.Error(codes.NotFound, "no region")}
	conn := startFakeCompute(t, regions, &fakeZoneService{}, nil)
	c := NewRegionClient(conn)

	_, err := c.Get(ctxBackground(), "atlantis")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg), "expected ErrInvalidArg: %v", err)
}

func TestRegionClient_Get_PermissionDeniedMapsToInvalidArg(t *testing.T) {
	regions := &fakeRegionService{err: status.Error(codes.PermissionDenied, "scope filter")}
	conn := startFakeCompute(t, regions, &fakeZoneService{}, nil)
	c := NewRegionClient(conn)

	_, err := c.Get(ctxBackground(), "secret-region")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestRegionClient_Get_Unavailable(t *testing.T) {
	regions := &fakeRegionService{err: status.Error(codes.Unavailable, "compute down")}
	conn := startFakeCompute(t, regions, &fakeZoneService{}, nil)
	c := NewRegionClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Get(ctx, "ru-central1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestRegionClient_Get_EmptyID(t *testing.T) {
	c := NewRegionClient(startFakeCompute(t, &fakeRegionService{}, &fakeZoneService{}, nil))
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestRegionClient_NilConn(t *testing.T) {
	assert.Nil(t, NewRegionClient(nil))
}
