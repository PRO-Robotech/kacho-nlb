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

	geopb "github.com/PRO-Robotech/kacho-geo/proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeRegionService — in-memory geo.RegionServiceServer.
type fakeRegionService struct {
	geopb.UnimplementedRegionServiceServer

	resp *geopb.Region
	err  error

	gotReq *geopb.GetRegionRequest
}

func (f *fakeRegionService) Get(_ context.Context, req *geopb.GetRegionRequest) (*geopb.Region, error) {
	f.gotReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestRegionClient_Get_HappyPath(t *testing.T) {
	regions := &fakeRegionService{resp: &geopb.Region{Id: "ru-central1", Name: "Central Russia"}}
	conn := startFakeGeo(t, regions)
	c := NewRegionClient(conn)
	require.NotNil(t, c)

	got, err := c.Get(ctxBackground(), "ru-central1")
	require.NoError(t, err)
	assert.Equal(t, "ru-central1", got.ID)
	assert.Equal(t, "Central Russia", got.Name)
	// Stateless pass-through: ровно один RegionService.Get-вызов с тем же region_id;
	// никакого ZoneService.List (kacho-nlb region-precheck зон не перечисляет).
	require.NotNil(t, regions.gotReq)
	assert.Equal(t, "ru-central1", regions.gotReq.GetRegionId())
}

func TestRegionClient_Get_NotFoundMapsToInvalidArg(t *testing.T) {
	regions := &fakeRegionService{err: status.Error(codes.NotFound, "no region")}
	conn := startFakeGeo(t, regions)
	c := NewRegionClient(conn)

	_, err := c.Get(ctxBackground(), "atlantis")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg), "expected ErrInvalidArg: %v", err)
}

func TestRegionClient_Get_PermissionDeniedMapsToInvalidArg(t *testing.T) {
	regions := &fakeRegionService{err: status.Error(codes.PermissionDenied, "scope filter")}
	conn := startFakeGeo(t, regions)
	c := NewRegionClient(conn)

	_, err := c.Get(ctxBackground(), "secret-region")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestRegionClient_Get_Unavailable(t *testing.T) {
	regions := &fakeRegionService{err: status.Error(codes.Unavailable, "geo down")}
	conn := startFakeGeo(t, regions)
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
	c := NewRegionClient(startFakeGeo(t, &fakeRegionService{}))
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestRegionClient_NilConn(t *testing.T) {
	assert.Nil(t, NewRegionClient(nil))
}

func TestNewRegionClientFromStubs_NilStub(t *testing.T) {
	assert.Nil(t, NewRegionClientFromStubs(nil))
}
