// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Каждый sentinel должен оставаться identity-сравнимым через errors.Is,
// чтобы service/mapRepoErr правильно маппил wrapping-цепочки в gRPC-коды.
func TestSentinels_IdentityViaIs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sentinel error
	}{
		{"ErrNotFound", domain.ErrNotFound},
		{"ErrAlreadyExists", domain.ErrAlreadyExists},
		{"ErrFailedPrecondition", domain.ErrFailedPrecondition},
		{"ErrInvalidArg", domain.ErrInvalidArg},
		{"ErrInternal", domain.ErrInternal},
		{"ErrUnavailable", domain.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("repo layer: %w", tc.sentinel)
			if !errors.Is(wrapped, tc.sentinel) {
				t.Fatalf("errors.Is failed for %q via wrapping chain", tc.name)
			}
		})
	}
}

func TestSentinels_AreDistinct(t *testing.T) {
	t.Parallel()
	// Sanity: разные sentinel'ы не должны быть identity-equal.
	if errors.Is(domain.ErrNotFound, domain.ErrAlreadyExists) {
		t.Fatal("ErrNotFound must not equal ErrAlreadyExists")
	}
	if errors.Is(domain.ErrInternal, domain.ErrUnavailable) {
		t.Fatal("ErrInternal must not equal ErrUnavailable")
	}
}
