package targetgroup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestMapDomainErr(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"domain NotFound", fmt.Errorf("%w: TargetGroup tgr-x not found", domain.ErrNotFound), codes.NotFound},
		{"repo NotFound", fmt.Errorf("%w: TargetGroup tgr-y not found", kachorepo.ErrNotFound), codes.NotFound},
		{"domain AlreadyExists", fmt.Errorf("%w: name dup", domain.ErrAlreadyExists), codes.AlreadyExists},
		{"domain FailedPrecondition", fmt.Errorf("%w: TG is being deleted", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		{"domain InvalidArg", fmt.Errorf("%w: bad weight", domain.ErrInvalidArg), codes.InvalidArgument},
		{"domain Unavailable", fmt.Errorf("%w: peer down", domain.ErrUnavailable), codes.Unavailable},
		{"domain Internal", fmt.Errorf("%w: pgx-leak", domain.ErrInternal), codes.Internal},
		{"unknown → Internal", errors.New("anonymous"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mapDomainErr(c.in)
			if c.want == codes.OK {
				assert.Nil(t, err)
				return
			}
			assert.Equal(t, c.want, status.Code(err))
		})
	}
}

func TestMapDomainErr_PassesThroughGRPCStatus(t *testing.T) {
	// status.Error remains as-is.
	in := status.Errorf(codes.PermissionDenied, "no")
	out := mapDomainErr(in)
	assert.Equal(t, codes.PermissionDenied, status.Code(out))
}

func TestPeerErrToStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"NotFound → InvalidArgument", fmt.Errorf("%w: not found", domain.ErrNotFound), codes.InvalidArgument},
		{"InvalidArg → InvalidArgument", fmt.Errorf("%w: bad", domain.ErrInvalidArg), codes.InvalidArgument},
		{"FailedPrecondition", fmt.Errorf("%w: precond", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		{"Unavailable", fmt.Errorf("%w: down", domain.ErrUnavailable), codes.Unavailable},
		{"unknown → Internal", errors.New("?"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := peerErrToStatus(c.err, "project", "prj-x")
			if c.want == codes.OK {
				assert.Nil(t, err)
				return
			}
			assert.Equal(t, c.want, status.Code(err))
		})
	}
}

func TestRegionFromZone(t *testing.T) {
	cases := []struct {
		zone, region string
	}{
		{"ru-central1-a", "ru-central1"},
		{"ru-central2-b", "ru-central2"},
		{"eu-west1-c", "eu-west1"},
		{"", ""},
		{"no-dash", "no-dash"}, // unparseable → as-is
	}
	for _, c := range cases {
		assert.Equalf(t, c.region, regionFromZone(c.zone), "zone=%q", c.zone)
	}
}
