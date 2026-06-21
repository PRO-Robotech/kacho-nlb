BINARY_API     := kacho-loadbalancer
BINARY_MIG     := migrator
CMD_API        := ./cmd/kacho-loadbalancer
CMD_MIG        := ./cmd/migrator
IMAGE          := kacho-nlb:dev

.PHONY: build build-api build-migrator test test-short vet lint docker sync-migrations helm-lint helm-render-guard audit-list-filter

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
