# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

BINARY_API     := kacho-loadbalancer
BINARY_MIG     := migrator
CMD_API        := ./cmd/kacho-loadbalancer
CMD_MIG        := ./cmd/migrator
IMAGE          := kacho-nlb:dev

.PHONY: build build-api build-migrator test test-short vet lint docker sync-migrations helm-lint helm-render-guard audit-list-filter proto-install-plugins proto-vendor proto-lint proto-gen

build: build-api build-migrator

build-api:
	CGO_ENABLED=0 go build -o bin/$(BINARY_API) $(CMD_API)

build-migrator:
	CGO_ENABLED=0 go build -o bin/$(BINARY_MIG) $(CMD_MIG)

test:
	go test ./... -race -cover -timeout 300s

test-short:
	go test ./... -race -cover -short -timeout 120s

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# proto-install-plugins — ставит protoc-плагины в $GOBIN (lookup через $PATH для buf).
# Доменный proto kacho-nlb генерируется этими тремя плагинами; permission-catalog для nlb —
# hand-written (internal/check/permission_map.go), buf-catalog-плагин не нужен.
proto-install-plugins:
	go install google.golang.org/protobuf/cmd/protoc-gen-go
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway

# proto-vendor — подтягивает corelib-owned инфра-протосы (operation/validation/
# authz_options/cloud-api + google/*) из kacho-corelib в proto/ ТОЛЬКО для buf-резолва
# импортов доменного proto. Единственный источник этих файлов — kacho-corelib; здесь они
# gitignored и не коммитятся (их Go-stubs тоже живут в kacho-corelib, дублей нет). Цель
# подтягивает их локально перед buf lint/generate.
CORELIB_PROTO ?= ../kacho-corelib/proto
VENDORED_INFRA_PROTOS = \
	google/api/annotations.proto \
	google/api/http.proto \
	google/rpc/status.proto \
	kacho/cloud/api/operation.proto \
	kacho/cloud/operation/operation.proto \
	kacho/cloud/validation.proto \
	kacho/iam/authz/v1/authz_options.proto

proto-vendor:
	@for f in $(VENDORED_INFRA_PROTOS); do \
		mkdir -p "proto/$$(dirname $$f)"; \
		cp "$(CORELIB_PROTO)/$$f" "proto/$$f"; \
	done

proto-lint: proto-vendor
	cd proto && buf lint

# proto-gen — регенерация Go-stubs доменного proto nlb (kacho/cloud/loadbalancer/v1) из
# proto/. Универсальная ИНФРА (operation/validation/authz_options/cloud-api/google)
# подтягивается proto-vendor только для buf-резолва импортов и НЕ генерируется (Go-stubs
# живут в kacho-corelib / canonical genproto) — см. proto/buf.gen.yaml inputs.paths.
proto-gen: proto-vendor
	cd proto && buf generate

# audit-list-filter — RBAC sub-phase D §11 (issue #111) CI gate. Asserts every
# public List<Resource> use-case filters per-object through authzfilter.Filter
# (kacho-iam AuthorizeService.ListObjects backend). security.md: публичный
# List<Resource> обязан фильтровать результат через listauthz.
audit-list-filter:
	@./tools/audit-list-filter.sh

sync-migrations:
	cp ../kacho-corelib/migrations/common/*.sql internal/migrations/

docker:
	cd .. && docker build -f kacho-nlb/Dockerfile -t $(IMAGE) .

# ---- Helm deploy-chart render guards (offline `helm template`/`lint`) -------

helm-lint:
	helm lint deploy/ --set db.password=test

# Asserts cross-service peer-edge wiring (vpc/compute/iam/geo) actually renders
# into config.yaml + mTLS. Pure chart render — no cluster contact.
helm-render-guard:
	bash deploy/tests/render-guard.sh

.PHONY: migrate-up migrate-down migrate-status
migrate-up:
	KACHO_NLB_REPOSITORY__POSTGRES__URL=$$DSN bin/$(BINARY_MIG) up --dialect=postgres --dsn=$$DSN

migrate-down:
	KACHO_NLB_REPOSITORY__POSTGRES__URL=$$DSN bin/$(BINARY_MIG) down --dialect=postgres --dsn=$$DSN

migrate-status:
	KACHO_NLB_REPOSITORY__POSTGRES__URL=$$DSN bin/$(BINARY_MIG) status --dialect=postgres --dsn=$$DSN

.PHONY: run
run: build-api
	bin/$(BINARY_API) serve

# ---- Newman regression -----------------------------------------------------

.PHONY: gen-newman test-newman test-newman-incremental validate-newman

gen-newman:
	cd tests/newman && python3 scripts/gen.py

validate-newman:
	cd tests/newman && python3 scripts/validate-cases.py

test-newman: gen-newman
	cd tests/newman && ./scripts/run.sh

test-newman-incremental: gen-newman
	cd tests/newman && ./scripts/run-incremental.sh
