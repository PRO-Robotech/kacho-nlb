// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package shared

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/filter"
)

// nameField is the only whitelisted filter field in the current NLB API surface
// (filter — kacho-corelib/filter.Parse с whitelist полей; текущий whitelist — name=).
const nameField = "name"

// ParseNameFilter parses the request `filter` string and returns the requested
// `name` equality value, or "" when no filter is set.
//
// It is the single source of truth for name= filtering across all NLB List
// use-cases (NetworkLoadBalancer / TargetGroup / Listener), replacing three
// divergent local parsers. It delegates to kacho-corelib/filter.Parse so the
// grammar and error texts match every other Kachō service.
//
// Contract:
//   - empty input            → ("", nil)            // no filter
//   - name="value"           → ("value", nil)
//   - unknown field / unquoted / malformed → InvalidArgument
//
// A malformed or unknown-field filter is a client error → InvalidArgument
// (never silently dropped, which would widen the result set unexpectedly).
func ParseNameFilter(raw string) (string, error) {
	ast, err := filter.Parse(raw, []string{nameField})
	if err != nil {
		return "", status.Error(codes.InvalidArgument, err.Error())
	}
	if ast == nil {
		return "", nil // empty filter → no name predicate
	}
	return ast.Value, nil
}
