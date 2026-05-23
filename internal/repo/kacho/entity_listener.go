package kacho

import (
	"time"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// ListenerRecord — repo-entity Listener. domain.Listener + DB-managed CreatedAt/UpdatedAt.
type ListenerRecord struct {
	domain.Listener
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListenerFilter — фильтр для List listeners.
//
// LoadBalancerID — partition по родительскому LB; используется в ListByLB.
// ProjectID — FGA scoping. Name — точное совпадение.
type ListenerFilter struct {
	ProjectID      string
	LoadBalancerID string
	Name           string
	Filter         string
}
