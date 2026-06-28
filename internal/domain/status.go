// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"

// Все enum-newtypes для статусов / типов NLB.
//
// Inline-литералы `"ACTIVE"` / `"CREATING"` / `"EXTERNAL"` в Go-коде вне
// этого файла — запрещены (запрет inline-status).
// Use-case-/handler-/repo-слои сравнивают только через эти именованные
// константы.

// ---- LBType ----------------------------------------------------------------

type LBType string

const (
	LBTypeExternal LBType = "EXTERNAL"
	LBTypeInternal LBType = "INTERNAL"
)

func (t LBType) Validate() error {
	switch t {
	case LBTypeExternal, LBTypeInternal:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("type", "type must be one of: EXTERNAL, INTERNAL").
		Err()
}

// ---- LBStatus --------------------------------------------------------------

type LBStatus string

const (
	LBStatusCreating LBStatus = "CREATING"
	LBStatusStarting LBStatus = "STARTING"
	LBStatusActive   LBStatus = "ACTIVE"
	LBStatusStopping LBStatus = "STOPPING"
	LBStatusStopped  LBStatus = "STOPPED"
	LBStatusDeleting LBStatus = "DELETING"
	LBStatusInactive LBStatus = "INACTIVE"
)

func (s LBStatus) Validate() error {
	switch s {
	case LBStatusCreating, LBStatusStarting, LBStatusActive,
		LBStatusStopping, LBStatusStopped, LBStatusDeleting, LBStatusInactive:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid LoadBalancer status").
		Err()
}

// ---- SessionAffinity -------------------------------------------------------

type SessionAffinity string

const (
	SessionAffinity5Tuple       SessionAffinity = "FIVE_TUPLE"
	SessionAffinityClientIPOnly SessionAffinity = "CLIENT_IP_ONLY"
)

func (a SessionAffinity) Validate() error {
	switch a {
	case SessionAffinity5Tuple, SessionAffinityClientIPOnly:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("session_affinity",
			"session_affinity must be one of: FIVE_TUPLE, CLIENT_IP_ONLY").
		Err()
}

// ---- ListenerStatus --------------------------------------------------------

type ListenerStatus string

const (
	ListenerStatusCreating ListenerStatus = "CREATING"
	ListenerStatusActive   ListenerStatus = "ACTIVE"
	ListenerStatusUpdating ListenerStatus = "UPDATING"
	ListenerStatusDeleting ListenerStatus = "DELETING"
)

func (s ListenerStatus) Validate() error {
	switch s {
	case ListenerStatusCreating, ListenerStatusActive,
		ListenerStatusUpdating, ListenerStatusDeleting:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid Listener status").
		Err()
}

// ---- TargetGroupStatus -----------------------------------------------------

type TargetGroupStatus string

const (
	TargetGroupStatusActive   TargetGroupStatus = "ACTIVE"
	TargetGroupStatusDeleting TargetGroupStatus = "DELETING"
)

func (s TargetGroupStatus) Validate() error {
	switch s {
	case TargetGroupStatusActive, TargetGroupStatusDeleting:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid TargetGroup status").
		Err()
}

// ---- TargetHealthStatus ----------------------------------------------------
// (per-target observed health; computed в GetTargetStates.)

type TargetHealthStatus string

const (
	TargetHealthInitial   TargetHealthStatus = "INITIAL"
	TargetHealthHealthy   TargetHealthStatus = "HEALTHY"
	TargetHealthUnhealthy TargetHealthStatus = "UNHEALTHY"
	TargetHealthDraining  TargetHealthStatus = "DRAINING"
	TargetHealthInactive  TargetHealthStatus = "INACTIVE"
)

func (s TargetHealthStatus) Validate() error {
	switch s {
	case TargetHealthInitial, TargetHealthHealthy, TargetHealthUnhealthy,
		TargetHealthDraining, TargetHealthInactive:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid Target health status").
		Err()
}

// ---- HealthCheckProto ------------------------------------------------------
// (тип probe внутри HealthCheck — 4-way oneof; конкретные option-структуры —
// в health_check.go.)

type HealthCheckProto string

const (
	HealthCheckProtoTCP   HealthCheckProto = "TCP"
	HealthCheckProtoHTTP  HealthCheckProto = "HTTP"
	HealthCheckProtoHTTPS HealthCheckProto = "HTTPS"
	HealthCheckProtoGRPC  HealthCheckProto = "GRPC"
)

func (p HealthCheckProto) Validate() error {
	switch p {
	case HealthCheckProtoTCP, HealthCheckProtoHTTP, HealthCheckProtoHTTPS, HealthCheckProtoGRPC:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("health_check",
			"health_check protocol must be one of: TCP, HTTP, HTTPS, GRPC").
		Err()
}
