// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import "time"

// Magic-numbers и enum-литералы для domain-слоя (
// запрет inline-status / inline-magic-numbers в use-case-/handler-коде).
// Источник истины для всего, что выглядит как «магическая константа».

// ---- Размерные лимиты ------------------------------------------------------

const (
	// ShortIDLen — длина prefix-сегмента id, используемого в derived-именах
	// (например default-target-group-<8chars>). Зеркалит kacho-vpc.
	ShortIDLen = 8

	// MaxDescriptionLen — UTF-8 rune-count лимит description.
	MaxDescriptionLen = 256

	// MaxLabelPairs — cardinality limit для LbLabels.
	MaxLabelPairs = 64

	// MaxLabelKeyLen / MaxLabelValueLen — границы длины label key/value
	// (в байтах; regex отдельно).
	MaxLabelKeyLen   = 63
	MaxLabelValueLen = 63
)

// ---- Port / weight ---------------------------------------------------------

const (
	// PortMin / PortMax — границы TCP/UDP-порта (LbPort.Validate).
	PortMin LbPort = 1
	PortMax LbPort = 65535

	// MaxTargetWeight — верхняя граница weight таргета.
	// 0 разрешён и означает «drain effectively без remove».
	MaxTargetWeight LbWeight = 1000
)

// ---- HealthCheck defaults / границы ----------------------------------------

const (
	// DefaultHealthInterval / DefaultHealthTimeout —
	DefaultHealthInterval LbDuration = LbDuration(2 * time.Second)
	DefaultHealthTimeout  LbDuration = LbDuration(1 * time.Second)

	// HealthIntervalMin / Max —.
	HealthIntervalMin LbDuration = LbDuration(1 * time.Second)
	HealthIntervalMax LbDuration = LbDuration(600 * time.Second)

	// HealthTimeoutMin / Max — нижняя граница 1ms (positive), верхняя — не
	// больше interval. interval-comparison делается в HealthCheck.Validate.
	HealthTimeoutMin LbDuration = LbDuration(1 * time.Millisecond)

	// HealthThresholdMin / Max — [2..10].
	HealthThresholdMin int32 = 2
	HealthThresholdMax int32 = 10

	// DefaultUnhealthyThreshold / DefaultHealthyThreshold —
	DefaultUnhealthyThreshold int32 = 2
	DefaultHealthyThreshold   int32 = 2
)

// ---- Target group lifecycle -------------------------------------------------

const (
	// DefaultDeregistrationDelay — (300s).
	DefaultDeregistrationDelay int32 = 300

	// DeregistrationDelayMin / Max — [0..3600].
	DeregistrationDelayMin int32 = 0
	DeregistrationDelayMax int32 = 3600

	// DefaultSlowStart — (0s = выключен).
	DefaultSlowStart int32 = 0

	// SlowStartMin / Max — [0..900].
	SlowStartMin int32 = 0
	SlowStartMax int32 = 900

	// DefaultTargetWeight — (100).
	DefaultTargetWeight LbWeight = 100
)

// ---- Cardinality лимиты ----------------------------------------------------

const (
	// MaxTargetsPerGroup —; защита от raid'а ресурса DB.
	MaxTargetsPerGroup = 100

	// MaxListenersPerLB —
	MaxListenersPerLB = 50
)

// ---- Enum-литералы для свободных строковых newtypes -----------------------
// (inline `"TCP"` / `"IPV4"` в use-case-коде запрещён;
// сравниваем через эти именованные константы.)

const (
	ProtoTCP LbProto = "TCP"
	ProtoUDP LbProto = "UDP"

	IPVersionV4 IPVersion = "IPV4"
	IPVersionV6 IPVersion = "IPV6"
)
