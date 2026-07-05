// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package vpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	refpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/reference"
	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeAddressService — in-memory AddressServiceServer (Get only; других
// тестируем через InternalAddressClient).
type fakeAddressService struct {
	vpcpb.UnimplementedAddressServiceServer

	resp *vpcpb.Address
	err  error
}

func (f *fakeAddressService) Get(_ context.Context, _ *vpcpb.GetAddressRequest) (*vpcpb.Address, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestAddressClient_Get_ExternalIPv4Used(t *testing.T) {
	addr := &vpcpb.Address{
		Id:        "e9b-ip-1",
		ProjectId: "prj-1",
		Used:      true,
		Address: &vpcpb.Address_ExternalIpv4Address{
			ExternalIpv4Address: &vpcpb.ExternalIpv4Address{
				Address: "203.0.113.5",
				ZoneId:  "ru-central1-a",
			},
		},
		UsedBy: []*refpb.Reference{
			{Referrer: &refpb.Referrer{Type: "nlb_listener", Id: "lst-1"}},
		},
	}
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{resp: addr}, nil, nil)
	c := NewAddressClient(conn)

	got, err := c.Get(ctxBackground(), "e9b-ip-1")
	require.NoError(t, err)
	assert.Equal(t, "e9b-ip-1", got.ID)
	assert.Equal(t, "prj-1", got.ProjectID)
	assert.Equal(t, "203.0.113.5", got.Value)
	assert.Equal(t, AddressFamilyIPv4, got.Family)
	assert.True(t, got.External)
	require.NotNil(t, got.UsedBy)
	assert.Equal(t, "nlb_listener", got.UsedBy.Kind)
	assert.Equal(t, "lst-1", got.UsedBy.ID)
}

func TestAddressClient_Get_InternalIPv4Free(t *testing.T) {
	addr := &vpcpb.Address{
		Id:        "e9b-ip-2",
		ProjectId: "prj-1",
		Used:      false,
		Address: &vpcpb.Address_InternalIpv4Address{
			InternalIpv4Address: &vpcpb.InternalIpv4Address{
				Address: "10.128.0.42",
				Scope:   &vpcpb.InternalIpv4Address_SubnetId{SubnetId: "e9b-1"},
			},
		},
	}
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{resp: addr}, nil, nil)
	c := NewAddressClient(conn)

	got, err := c.Get(ctxBackground(), "e9b-ip-2")
	require.NoError(t, err)
	assert.Equal(t, "10.128.0.42", got.Value)
	assert.Equal(t, AddressFamilyIPv4, got.Family)
	assert.False(t, got.External)
	assert.Nil(t, got.UsedBy)
}

func TestAddressClient_Get_InternalIPv6(t *testing.T) {
	addr := &vpcpb.Address{
		Id:        "e9b-ip-3",
		ProjectId: "prj-1",
		Address: &vpcpb.Address_InternalIpv6Address{
			InternalIpv6Address: &vpcpb.InternalIpv6Address{
				Address: "fd00::42",
				Scope:   &vpcpb.InternalIpv6Address_SubnetId{SubnetId: "e9b-1"},
			},
		},
	}
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{resp: addr}, nil, nil)
	c := NewAddressClient(conn)

	got, err := c.Get(ctxBackground(), "e9b-ip-3")
	require.NoError(t, err)
	assert.Equal(t, "fd00::42", got.Value)
	assert.Equal(t, AddressFamilyIPv6, got.Family)
}

func TestAddressClient_Get_ExternalIPv6(t *testing.T) {
	addr := &vpcpb.Address{
		Id:        "e9b-ext6",
		ProjectId: "prj-1",
		Address: &vpcpb.Address_ExternalIpv6Address{
			ExternalIpv6Address: &vpcpb.ExternalIpv6Address{
				Address: "2001:db8::abcd",
				ZoneId:  "ru-central1-a",
			},
		},
	}
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{resp: addr}, nil, nil)
	c := NewAddressClient(conn)

	got, err := c.Get(ctxBackground(), "e9b-ext6")
	require.NoError(t, err)
	assert.Equal(t, "2001:db8::abcd", got.Value)
	assert.Equal(t, AddressFamilyIPv6, got.Family)
	assert.True(t, got.External, "external_ipv6 must surface External=true (BYO v6 external)")
}

func TestAddressClient_Get_NotFound(t *testing.T) {
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{err: status.Error(codes.NotFound, "no address")},
		nil, nil)
	c := NewAddressClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestAddressClient_Get_PermissionDenied(t *testing.T) {
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{err: status.Error(codes.PermissionDenied, "scope")},
		nil, nil)
	c := NewAddressClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestAddressClient_Get_Unavailable(t *testing.T) {
	conn := startFakeVPC(t, nil, nil, &fakeAddressService{err: status.Error(codes.Unavailable, "down")},
		nil, nil)
	c := NewAddressClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "e9b-1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestAddressClient_Get_EmptyID(t *testing.T) {
	c := NewAddressClient(startFakeVPC(t, nil, nil, &fakeAddressService{}, nil, nil))
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestAddressClient_NilConn(t *testing.T) { assert.Nil(t, NewAddressClient(nil)) }
