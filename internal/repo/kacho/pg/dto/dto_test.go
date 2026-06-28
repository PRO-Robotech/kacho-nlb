// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package dto

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestLabelsRoundtrip(t *testing.T) {
	in := domain.LabelsFromMap(map[string]string{
		"env":     "prod",
		"region":  "eu-west",
		"version": "v1.2.3",
	})
	b, err := LabelsToJSONB(in)
	require.NoError(t, err)
	require.NotEmpty(t, b)

	out, err := LabelsFromJSONB(b)
	require.NoError(t, err)
	assert.Equal(t, 3, out.Len())
	env, ok := out.Get("env")
	require.True(t, ok)
	assert.Equal(t, domain.LbLabelVal("prod"), env)
}

func TestLabelsNilEmpty(t *testing.T) {
	b, err := LabelsToJSONB(domain.LbLabels{})
	require.NoError(t, err)
	assert.Equal(t, "{}", string(b))

	out, err := LabelsFromJSONB(nil)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Len())

	out2, err := LabelsFromJSONB([]byte("null"))
	require.NoError(t, err)
	assert.Equal(t, 0, out2.Len())
}

func TestHealthCheckTCP_Roundtrip(t *testing.T) {
	in := domain.HealthCheck{
		Name:               "tcp-hc",
		Interval:           domain.LbDuration(3 * time.Second),
		Timeout:            domain.LbDuration(time.Second),
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		TCP:                &domain.HealthCheckTCP{Port: 8443},
	}
	b, err := HealthCheckToJSONB(in)
	require.NoError(t, err)
	out, err := HealthCheckFromJSONB(b)
	require.NoError(t, err)
	assert.Equal(t, in.Name, out.Name)
	assert.Equal(t, in.Interval, out.Interval)
	assert.Equal(t, in.UnhealthyThreshold, out.UnhealthyThreshold)
	require.NotNil(t, out.TCP)
	assert.Equal(t, domain.LbPort(8443), out.TCP.Port)
}

func TestHealthCheckHTTPS_Roundtrip(t *testing.T) {
	in := domain.HealthCheck{
		Name:               "https-hc",
		Interval:           domain.LbDuration(5 * time.Second),
		Timeout:            domain.LbDuration(2 * time.Second),
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
		HTTPS: &domain.HealthCheckHTTPS{
			Port:             443,
			Path:             "/_health",
			ExpectedStatuses: []int32{200, 204},
		},
	}
	b, err := HealthCheckToJSONB(in)
	require.NoError(t, err)
	out, err := HealthCheckFromJSONB(b)
	require.NoError(t, err)
	require.NotNil(t, out.HTTPS)
	assert.Equal(t, domain.LbPort(443), out.HTTPS.Port)
	assert.Equal(t, "/_health", out.HTTPS.Path)
	assert.Equal(t, []int32{200, 204}, out.HTTPS.ExpectedStatuses)
}

func TestHealthCheckZero_NoTypeRoundtrip(t *testing.T) {
	b, err := HealthCheckToJSONB(domain.HealthCheck{})
	require.NoError(t, err)
	assert.Equal(t, "{}", string(b))

	out, err := HealthCheckFromJSONB(b)
	require.NoError(t, err)
	assert.Empty(t, out.Name)
	assert.Nil(t, out.TCP)
	assert.Nil(t, out.HTTP)
}

func TestOptString_RoundTrip(t *testing.T) {
	type myID string
	v := OptFromStr[myID]("hello")
	val, ok := v.Maybe()
	require.True(t, ok)
	assert.Equal(t, myID("hello"), val)
	assert.Equal(t, "hello", OptString(v))

	empty := OptFromStr[myID]("")
	_, ok2 := empty.Maybe()
	assert.False(t, ok2, "empty string → None")
	assert.Equal(t, "", OptString(empty))
}
