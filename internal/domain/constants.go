package domain

import "time"

// Magic-numbers и enum-литералы для domain-слоя (evgeniy §D.9, §AP-2/AP-4 —
// запрет inline-status / inline-magic-numbers в use-case-/handler-коде).
// Источник истины для всего, что выглядит как «магическая константа».

// ---- Размерные лимиты ------------------------------------------------------

const (
	// ShortIDLen — длина prefix-сегмента id, используемого в derived-именах
	// (например default-target-group-<8chars>). Зеркалит kacho-vpc.
	ShortIDLen = 8

	// MaxDescriptionLen — UTF-8 rune-count лимит description.
	MaxDescriptionLen = 256

	// MaxLabelPairs — cardinality limit для LbLabels (acceptance §3 NLB-003).
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

	// MaxTargetWeight — верхняя граница weight таргета (acceptance TGT-005).
	// 0 разрешён и означает «drain effectively без remove» (design §2.5).
	MaxTargetWeight LbWeight = 1000
)

// ---- HealthCheck defaults / границы ----------------------------------------

const (
	// DefaultHealthInterval / DefaultHealthTimeout — design §2.5.
	DefaultHealthInterval LbDuration = LbDuration(2 * time.Second)
	DefaultHealthTimeout  LbDuration = LbDuration(1 * time.Second)

	// HealthIntervalMin / Max — acceptance TGR-005.
	HealthIntervalMin LbDuration = LbDuration(1 * time.Second)
	HealthIntervalMax LbDuration = LbDuration(600 * time.Second)

	// HealthTimeoutMin / Max — нижняя граница 1ms (positive), верхняя — не
	// больше interval. interval-comparison делается в HealthCheck.Validate.
	HealthTimeoutMin LbDuration = LbDuration(1 * time.Millisecond)

	// HealthThresholdMin / Max — acceptance TGR-006: [2..10].
	HealthThresholdMin int32 = 2
	HealthThresholdMax int32 = 10

	// DefaultUnhealthyThreshold / DefaultHealthyThreshold — design §2.5.
	DefaultUnhealthyThreshold int32 = 2
	DefaultHealthyThreshold   int32 = 2
)

// ---- Target group lifecycle -------------------------------------------------

const (
	// DefaultDeregistrationDelay — design §2.5 (300s).
	DefaultDeregistrationDelay int32 = 300

	// DeregistrationDelayMin / Max — acceptance TGR-007: [0..3600].
	DeregistrationDelayMin int32 = 0
	DeregistrationDelayMax int32 = 3600

	// DefaultSlowStart — design §2.5 (0s = выключен).
	DefaultSlowStart int32 = 0

	// SlowStartMin / Max — acceptance TGR-008: [0..900].
	SlowStartMin int32 = 0
	SlowStartMax int32 = 900

	// DefaultTargetWeight — design §2.5 (100).
	DefaultTargetWeight LbWeight = 100
)

// ---- Cardinality лимиты ----------------------------------------------------

const (
	// MaxTargetsPerGroup — design §2.5; защита от raid'а ресурса DB.
	MaxTargetsPerGroup = 100

	// MaxListenersPerLB — design §2.5.
	MaxListenersPerLB = 50
)

// ---- Enum-литералы для свободных строковых newtypes -----------------------
// (evgeniy §AP-2 — inline `"TCP"` / `"IPV4"` в use-case-коде запрещён;
// сравниваем через эти именованные константы.)

const (
	ProtoTCP LbProto = "TCP"
	ProtoUDP LbProto = "UDP"

	IPVersionV4 IPVersion = "IPV4"
	IPVersionV6 IPVersion = "IPV6"
)
