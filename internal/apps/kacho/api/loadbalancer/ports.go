package loadbalancer

import (
	computeclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Port-интерфейсы use-case-слоя NetworkLoadBalancer (Clean Architecture / evgeniy §2.B).
//
// Use-case'ы внутри пакета зависят ТОЛЬКО от этих port-ов; конкретные реализации
// (pgx-Repository, gRPC-typed-clients, FGA writer) инжектируются в composition
// root (`cmd/kacho-loadbalancer/main.go`). Тесты подменяют port'ы на ручные
// двойники (см. *_mock_test.go в этом же пакете).

// Repo — корневой CQRS-Repository kacho-nlb. Сохранён как алиас, чтобы handler-
// слой не импортировал leaf-пакет `repo/kacho` напрямую под другим именем —
// весь приём идёт через type Repo (читаемая локальная переменная для use-case'ов).
type Repo = kachorepo.Repository

// ProjectClient — Get(projectID) → *iamclient.Project. Используется sync-precheck
// в Create/Move use-case'ах для валидации `project_id` (`InvalidArgument` если
// peer вернул NotFound; `Unavailable` если peer недоступен).
type ProjectClient = iamclient.ProjectClient

// RegionClient — Get(regionID) → *computeclient.Region. Используется sync-precheck
// в Create use-case'е для валидации `region_id`.
type RegionClient = computeclient.RegionClient

// Logger — узкий port логгера; вся работа use-case'ов и worker'ов идёт через
// этот интерфейс — concrete *slog.Logger удовлетворяет его автоматически.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Debug(msg string, args ...any)
}
