// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package dto — pgmodel ↔ domain.X конвертация для kacho-nlb.
//
// Расположение: `internal/repo/kacho/pg/dto/`. Domain-пакет ничего не знает про
// JSONB-сериализацию (workspace CLAUDE.md «Чистая архитектура»); этот пакет
// единственное место, где доменные типы (HealthCheck, LbLabels) превращаются
// в JSONB-tape и обратно.
//
// Используется repo-impl'ом в `internal/repo/kacho/pg/`.
package dto

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/H-BF/corlib/pkg/option"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// LabelsToJSONB — domain.LbLabels → JSONB bytes. nil-map → `{}`.
func LabelsToJSONB(labels domain.LbLabels) ([]byte, error) {
	m := domain.LabelsToMap(labels)
	if m == nil {
		return []byte(`{}`), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal labels: %w", err)
	}
	return b, nil
}

// LabelsFromJSONB — JSONB bytes → domain.LbLabels.
//
// nil/empty → пустой LbLabels (т.к. domain считает empty == nil-map).
// jsonb-`null` → пустой LbLabels (паритет с psql NULL handling).
func LabelsFromJSONB(b []byte) (domain.LbLabels, error) {
	var labels domain.LbLabels
	if len(b) == 0 {
		return labels, nil
	}
	// jsonb может прийти как 'null' (4 байта) — это валидный JSON; интерпретируем
	// как пустой.
	if string(b) == "null" {
		return labels, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return labels, fmt.Errorf("unmarshal labels: %w", err)
	}
	return domain.LabelsFromMap(m), nil
}

// HealthCheckJSONB — wire-форма HealthCheck. Зеркалит proto HealthCheck
// (TCP / HTTP только; HTTPS/GRPC в domain — но в proto vendored их нет, см.
// kacho-proto/health_check.pb.go). HTTPS/GRPC сериализуются как
// {"type":"HTTPS"|"GRPC",...}, на reverse-чтение восстанавливаются в domain.
type HealthCheckJSONB struct {
	Name               string           `json:"name,omitempty"`
	IntervalNs         int64            `json:"interval_ns,omitempty"`
	TimeoutNs          int64            `json:"timeout_ns,omitempty"`
	UnhealthyThreshold int32            `json:"unhealthy_threshold,omitempty"`
	HealthyThreshold   int32            `json:"healthy_threshold,omitempty"`
	TCP                *HCPortOnly      `json:"tcp,omitempty"`
	HTTP               *HCPortPath      `json:"http,omitempty"`
	HTTPS              *HCPortPath      `json:"https,omitempty"`
	GRPC               *HCPortServiceNm `json:"grpc,omitempty"`
}

// HCPortOnly — TCP-probe (port only).
type HCPortOnly struct {
	Port int32 `json:"port"`
}

// HCPortPath — HTTP/HTTPS-probe.
type HCPortPath struct {
	Port             int32   `json:"port"`
	Path             string  `json:"path,omitempty"`
	ExpectedStatuses []int32 `json:"expected_statuses,omitempty"`
}

// HCPortServiceNm — gRPC-probe.
type HCPortServiceNm struct {
	Port        int32  `json:"port"`
	ServiceName string `json:"service_name,omitempty"`
}

// HealthCheckToJSONB — domain.HealthCheck → JSONB bytes. zero-value HC → `{}`.
func HealthCheckToJSONB(hc domain.HealthCheck) ([]byte, error) {
	if isHealthCheckZero(hc) {
		return []byte(`{}`), nil
	}
	wire := HealthCheckJSONB{
		Name:               string(hc.Name),
		IntervalNs:         int64(time.Duration(hc.Interval)),
		TimeoutNs:          int64(time.Duration(hc.Timeout)),
		UnhealthyThreshold: hc.UnhealthyThreshold,
		HealthyThreshold:   hc.HealthyThreshold,
	}
	switch {
	case hc.TCP != nil:
		wire.TCP = &HCPortOnly{Port: int32(hc.TCP.Port)}
	case hc.HTTP != nil:
		wire.HTTP = &HCPortPath{Port: int32(hc.HTTP.Port), Path: hc.HTTP.Path, ExpectedStatuses: hc.HTTP.ExpectedStatuses}
	case hc.HTTPS != nil:
		wire.HTTPS = &HCPortPath{Port: int32(hc.HTTPS.Port), Path: hc.HTTPS.Path, ExpectedStatuses: hc.HTTPS.ExpectedStatuses}
	case hc.GRPC != nil:
		wire.GRPC = &HCPortServiceNm{Port: int32(hc.GRPC.Port), ServiceName: hc.GRPC.ServiceName}
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal health_check: %w", err)
	}
	return b, nil
}

// HealthCheckFromJSONB — JSONB bytes → domain.HealthCheck. Empty/null → zero HC.
func HealthCheckFromJSONB(b []byte) (domain.HealthCheck, error) {
	var hc domain.HealthCheck
	if len(b) == 0 || string(b) == "null" || string(b) == "{}" {
		return hc, nil
	}
	var wire HealthCheckJSONB
	if err := json.Unmarshal(b, &wire); err != nil {
		return hc, fmt.Errorf("unmarshal health_check: %w", err)
	}
	hc.Name = domain.LbName(wire.Name)
	hc.Interval = domain.LbDuration(time.Duration(wire.IntervalNs))
	hc.Timeout = domain.LbDuration(time.Duration(wire.TimeoutNs))
	hc.UnhealthyThreshold = wire.UnhealthyThreshold
	hc.HealthyThreshold = wire.HealthyThreshold
	switch {
	case wire.TCP != nil:
		hc.TCP = &domain.HealthCheckTCP{Port: domain.LbPort(wire.TCP.Port)}
	case wire.HTTP != nil:
		hc.HTTP = &domain.HealthCheckHTTP{Port: domain.LbPort(wire.HTTP.Port), Path: wire.HTTP.Path, ExpectedStatuses: wire.HTTP.ExpectedStatuses}
	case wire.HTTPS != nil:
		hc.HTTPS = &domain.HealthCheckHTTPS{Port: domain.LbPort(wire.HTTPS.Port), Path: wire.HTTPS.Path, ExpectedStatuses: wire.HTTPS.ExpectedStatuses}
	case wire.GRPC != nil:
		hc.GRPC = &domain.HealthCheckGRPC{Port: domain.LbPort(wire.GRPC.Port), ServiceName: wire.GRPC.ServiceName}
	}
	return hc, nil
}

// isHealthCheckZero — true если HC не имеет ни одного set-поля (используется
// при сохранении пустого HC: пишем `{}` вместо полного wire-объекта с
// нулями).
func isHealthCheckZero(hc domain.HealthCheck) bool {
	return hc.Name == "" &&
		hc.Interval == 0 &&
		hc.Timeout == 0 &&
		hc.UnhealthyThreshold == 0 &&
		hc.HealthyThreshold == 0 &&
		hc.TCP == nil && hc.HTTP == nil && hc.HTTPS == nil && hc.GRPC == nil
}

// OptString — option.ValueOf[T] → *string для DB. Some("") и None
// различаются: Some("") пишет пустую строку, None пишет NULL (для nullable
// колонок). В schemas с DEFAULT пустой строки и NOT NULL — caller использует empty-string
// для None (см. ListenerOptToStr / OptToStr ниже).
func OptString[T ~string](v option.ValueOf[T]) string {
	if s, ok := v.Maybe(); ok {
		return string(s)
	}
	return ""
}

// OptFromStr — обратное: пустая строка → None; иначе Some(T(s)).
func OptFromStr[T ~string](s string) option.ValueOf[T] {
	if s == "" {
		return option.ValueOf[T]{}
	}
	return option.MustNewOption(T(s))
}
