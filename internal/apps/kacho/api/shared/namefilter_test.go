// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package shared

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ParseNameFilter is the single name= filter parser shared by all nlb List
// use-cases (loadbalancer / targetgroup / listener). It delegates to
// kacho-corelib/filter.Parse with the canonical whitelist {"name"} so the
// grammar + error texts are identical across resources (api-conventions:
// `filter` — kacho-corelib/filter.Parse с whitelist полей).
//
// Contract (reconciled from three divergent local parsers — see review report):
//   - empty input            → ("", nil)               // no filter
//   - name="value"           → ("value", nil)
//   - name = "value" (spaced) → ("value", nil)
//   - name=value (unquoted)  → InvalidArgument          // strict: value must be quoted
//   - unknown="x"            → InvalidArgument          // whitelist rejects unknown field
//   - garbage                → InvalidArgument
func TestParseNameFilter(t *testing.T) {
	t.Parallel()
	t.Run("valid", func(t *testing.T) {
		cases := map[string]string{
			``:                "",
			`name="edge"`:     "edge",
			`name="api-1"`:    "api-1",
			`name = "spaced"`: "spaced",
			`name=""`:         "",
		}
		for in, want := range cases {
			got, err := ParseNameFilter(in)
			if err != nil {
				t.Fatalf("ParseNameFilter(%q): unexpected err: %v", in, err)
			}
			if got != want {
				t.Fatalf("ParseNameFilter(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("invalid_arg", func(t *testing.T) {
		// Each of these diverged across the three former local parsers; the
		// unified strict contract rejects them all with InvalidArgument.
		for _, in := range []string{
			`name=edge`,   // unquoted value
			`name=`,       // no value
			`other="foo"`, // unknown field (whitelist)
			`garbage`,     // not a filter expression
			`name "edge"`, // missing operator
			`region="ru"`, // unknown field
		} {
			got, err := ParseNameFilter(in)
			if err == nil {
				t.Fatalf("ParseNameFilter(%q): expected InvalidArgument, got value %q", in, got)
			}
			if code := status.Code(err); code != codes.InvalidArgument {
				t.Fatalf("ParseNameFilter(%q): expected InvalidArgument, got %s (%v)", in, code, err)
			}
		}
	})
}
