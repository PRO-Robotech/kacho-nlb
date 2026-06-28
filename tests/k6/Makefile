# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# k6 load-test targets for kacho-nlb.
#
# Defaults assume port-forward of api-gateway to localhost:18080. Override
# with `make k6-baseline K6_BASE_URL=http://api-gateway.kacho.svc:8080`.
#
# Required env (see ../README.md):
#   IAM_TOKEN              - Bearer token from kacho-iam (CreateToken RPC)
#   EXISTING_PROJECT_ID    - kacho-iam project id (editor scope on it)
#   EXISTING_REGION_ID     - e.g. ru-central1
#   EXISTING_SUBNET_ID     - vpc subnet for INTERNAL listeners + ip_ref targets
#   EXISTING_ADDRESS_ID    - kacho-vpc Address for BYO VIP listener tests
#   EXISTING_INSTANCE_ID   - compute Instance for instance-id Target tests
#   EXISTING_LB_IDS        - comma-separated warm-set for read-heavy ops
#   EXISTING_TG_IDS        - comma-separated warm-set for read-heavy ops

K6              ?= k6
K6_BASE_URL     ?= http://localhost:18080
K6_OUT_DIR      ?= results
SCENARIOS_DIR   := scenarios

# Common k6 invocation. `--out json=...` produces an artefact suitable for
# version-controlled baseline tracking (workspace CLAUDE.md §VII.3).
define K6_RUN
	@mkdir -p $(K6_OUT_DIR)
	$(K6) run \
		--env K6_BASE_URL=$(K6_BASE_URL) \
		--out json=$(K6_OUT_DIR)/$(1)-$$(date -u +%Y%m%dT%H%M%S).json \
		$(SCENARIOS_DIR)/$(1).js
endef

.PHONY: help
help:
	@echo "k6 scenarios:"
	@echo "  make k6-smoke      - 100 RPS / 30s post-deploy sanity"
	@echo "  make k6-baseline   - 500 RPS / 5min full SLO assertions"
	@echo "  make k6-stress     - ramp 100->2000 RPS / 10min (breakpoint)"
	@echo "  make k6-soak       - 200 RPS x 60min (leak detection)"
	@echo "  make k6-spike      - 100/1500/100 RPS / 30s burst"
	@echo "  make k6-dry-run    - syntax-check all scenarios"
	@echo ""
	@echo "Variables: K6_BASE_URL=$(K6_BASE_URL)  K6_OUT_DIR=$(K6_OUT_DIR)"

.PHONY: k6-smoke
k6-smoke:
	$(call K6_RUN,smoke)

.PHONY: k6-baseline
k6-baseline:
	$(call K6_RUN,baseline)

.PHONY: k6-stress
k6-stress:
	$(call K6_RUN,stress)

.PHONY: k6-soak
k6-soak:
	$(call K6_RUN,soak)

.PHONY: k6-spike
k6-spike:
	$(call K6_RUN,spike)

.PHONY: k6-dry-run
k6-dry-run:
	@for s in smoke baseline stress soak spike; do \
		echo "==> $$s"; \
		$(K6) inspect --address '' $(SCENARIOS_DIR)/$$s.js || exit $$?; \
	done

.PHONY: clean
clean:
	rm -rf $(K6_OUT_DIR)/*.json
