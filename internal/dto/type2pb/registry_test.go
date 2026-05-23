package type2pb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestRegistry_AllPairsRegistered проверяет, что type-set generic constraint
// `Transferrable` действительно покрывает все ожидаемые пары — FindTransfer
// возвращает true для каждой.
func TestRegistry_AllPairsRegistered(t *testing.T) {
	t.Run("time", func(t *testing.T) {
		_, ok := dto.FindTransfer[time.Time, *timestamppb.Timestamp]()
		assert.True(t, ok)
	})
	t.Run("loadbalancer", func(t *testing.T) {
		_, ok := dto.FindTransfer[kachorepo.LoadBalancerRecord, *lbv1.NetworkLoadBalancer]()
		assert.True(t, ok)
	})
	t.Run("listener", func(t *testing.T) {
		_, ok := dto.FindTransfer[kachorepo.ListenerRecord, *lbv1.Listener]()
		assert.True(t, ok)
	})
	t.Run("target_group", func(t *testing.T) {
		_, ok := dto.FindTransfer[kachorepo.TargetGroupRecord, *lbv1.TargetGroup]()
		assert.True(t, ok)
	})
	t.Run("target", func(t *testing.T) {
		_, ok := dto.FindTransfer[kachorepo.TargetRecord, *lbv1.Target]()
		assert.True(t, ok)
	})
}

// TestRegistry_UnregisteredPairFails — Perform на незарегистрированной паре
// должен дать ошибку runtime. Compile-time type-set constraint мы не можем
// проверить из тест-кода (он constraint, а не runtime), но регистрация-lookup
// flow — да.
func TestRegistry_UnregisteredPairFails(t *testing.T) {
	type Bogus struct{ X int }
	type AlsoBogus struct{ Y int }
	_, ok := dto.FindTransfer[Bogus, AlsoBogus]()
	require.False(t, ok, "no transfer registered for Bogus → AlsoBogus")
}
