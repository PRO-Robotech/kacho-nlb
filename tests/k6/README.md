# kacho-nlb — k6 load tests

Load-test suite for the `kacho-nlb` control plane. Five scenarios cover the
SLO matrix from the design doc (`docs/superpowers/specs/2026-05-23-kacho-nlb-design.md` §7.4).

> Methodology and anti-patterns follow `.claude/skills/load-testing-coach`
> (workspace-level) and `kacho-vpc/.claude/agents/vpc-load-testing`.

## Layout

```
tests/k6/
├── scenarios/                 # 5 k6 scripts, one per scenario class
│   ├── smoke.js               # 100 RPS / 30s post-deploy sanity
│   ├── baseline.js            # 500 RPS / 5min full SLO assertions
│   ├── stress.js              # ramp 100->2000 RPS (breakpoint search)
│   ├── soak.js                # 200 RPS x 60min (leak detection)
│   └── spike.js               # 100/1500/100 RPS burst + recovery
├── lib/
│   ├── client.js              # base url, headers, REST routes, status helpers
│   ├── fixtures.js            # pre-seeded resource id loader (env-driven)
│   ├── dsl.js                 # createLB / createListener / addTargets / ...
│   ├── payloads.js            # request-body factories matching proto messages
│   ├── payload-templates.js   # merge layer over data/payload-templates.json
│   ├── mix.js                 # 60/20/10/10 workload mixer (design §7.4)
│   └── poll-op.js             # Operation LRO polling helper
├── data/
│   └── payload-templates.json # tunable defaults (no JS edit required)
├── ghz/                       # gRPC-direct K8s Jobs that bypass api-gateway
│   ├── README.md
│   ├── smoke-job.yaml
│   ├── baseline-job.yaml
│   ├── stress-job.yaml
│   └── spike-job.yaml
├── scripts/
│   └── run-k6.sh              # CI / shell runner for one or all scenarios
├── results/                   # gitignored output artefacts (.json per run)
├── Makefile                   # make k6-{smoke,baseline,stress,soak,spike}
└── README.md                  # this file
```

## Prerequisites

1. **k6** v0.50+: <https://k6.io/docs/get-started/installation/>.
   Verify `k6 version`.
2. **Deployed stack** with `kacho-nlb` reachable through `kacho-api-gateway`.
3. **Port-forward** api-gateway to `localhost:18080` (override via `K6_BASE_URL`):
   ```bash
   kubectl -n kacho port-forward svc/api-gateway 18080:8080
   ```
4. **IAM token** with `loadbalancer.*` permissions on the target project.
   Acquire via `kacho-iam` `CreateAccessToken` RPC and export:
   ```bash
   export IAM_TOKEN=...
   ```

## Environment variables

| Var | Required | Purpose |
|---|---|---|
| `K6_BASE_URL` / `BASE_URL` | no  | API gateway base URL. Default `http://localhost:18080`. |
| `IAM_TOKEN`                | yes | Bearer token (sent as `Authorization: Bearer ...`). |
| `ACTOR`                    | no  | Audit-trail actor id. Default `k6-load-test@kacho`. |
| `EXISTING_PROJECT_ID`      | yes | kacho-iam project id with editor scope. |
| `EXISTING_REGION_ID`       | yes | e.g. `ru-central1`. |
| `EXISTING_SUBNET_ID`       | opt | For Listener INTERNAL + `ip_ref` Target tests. |
| `EXISTING_ADDRESS_ID`      | opt | BYO VIP for Listener tests. |
| `EXISTING_INSTANCE_ID`     | opt | Compute Instance id for instance-id Target. |
| `EXISTING_NIC_ID`          | opt | NIC id for nic-id Target. |
| `EXISTING_ZONE_ID`         | opt | Zonal hint for ExternalIP Target. |
| `EXISTING_LB_IDS`          | opt | Comma-separated warm-set ids for read-heavy ops. |
| `EXISTING_TG_IDS`          | opt | Comma-separated warm-set ids for read-heavy ops. |

`scenarios/*.js` abort with a useful message if a required fixture is missing.

## How to run

Pick a scenario, run via Make or the shell script:

```bash
# from tests/k6/
make k6-smoke         # 100 RPS / 30s
make k6-baseline      # 500 RPS / 5min  — release gate
make k6-stress        # ramp to 2000 RPS
make k6-soak          # 60 min
make k6-spike         # burst to 1500 RPS

# alternative shell runner
scripts/run-k6.sh smoke
scripts/run-k6.sh baseline --quiet

# override base URL
K6_BASE_URL=http://api-gateway.kacho:8080 make k6-baseline

# CI dry-run (no live cluster needed; syntax + ast check)
make k6-dry-run
```

All runs write timestamped JSON output to `results/`, suitable for later
comparison with `jq` / `k6 cloud` / external analysers.

## SLO targets

Source: design doc §7.4. The k6 `thresholds` block in each scenario encodes
these as machine-checked invariants. Threshold breach -> non-zero exit -> CI
fail.

| Scenario | Profile                         | Pass criteria                                                                 |
|----------|---------------------------------|-------------------------------------------------------------------------------|
| smoke    | 100 RPS x 30s                   | `http_req_failed < 1%`; read p95 < 200ms                                      |
| baseline | 500 RPS x 5min                  | overall p95 <= 100ms, p99 <= 300ms; read p95 < 80ms; write p99 < 500ms; err < 1% |
| stress   | ramp 100 -> 2000 RPS / 10min    | run completes; err < 50%; read p99 < 5s — interpret curve manually            |
| soak     | 200 RPS x 60min                 | err < 0.5%; read p95 < 150ms; window drift (warmup vs cooldown) < 10%         |
| spike    | 100 / 1500 / 100 RPS / 30s burst| err < 5%; recovery-window read p95 < 300ms                                    |

### Workload mix (all scenarios)

Per design §7.4. Implemented in `lib/mix.js`.

- 60% reads — `NLB.Get`, `NLB.List`, `TG.Get`, `TG.List`
- 20% short Create+Delete — LB / Listener / TG
- 10% AddTargets / RemoveTargets
- 10% AttachTG / DetachTG

Reads dominate the latency distribution — that mirrors steady-state production
traffic, where the UI / control loops re-list every few seconds while writes
are user-driven.

## Interpreting results

| Symptom | Likely cause | Next step |
|---|---|---|
| `http_req_duration p99` > 5x baseline, p50 unchanged | GC pause or Postgres slow query (long tail) | grab pgxpool stats; check Postgres slow query log |
| `http_req_failed` rises with RPS (linear) | pool / fd exhaustion | check `pg_stat_activity`, kacho-nlb pgxpool `max_conns`, syscall fd count |
| Throughput plateau at low RPS, latency flat | single CPU-bound bottleneck (e.g. handler validate) | pprof CPU profile on the running pod |
| Memory growth across soak run | leak (pgx stmt cache, outbox cursor, prepared statements) | heap profile diff between warmup and cooldown |
| baseline OK, stress flat-lines at low RPS | api-gateway is the bottleneck, not nlb | switch to `ghz/baseline-job.yaml` for gRPC-direct comparison |
| Recovery latency stays high after spike | drain queue / dead worker / leaked goroutines | inspect `nlb_outbox` cursor and operation worker state |

### Variance budget

A single run is noisy. To call a regression, repeat 3x and compare:

- Variation **within 10%** between consecutive runs of the same build = noise.
- Variation **15-25%** across builds (same scenario) = regression candidate;
  re-run; if persistent, file a tech-debt ticket per workspace CLAUDE.md.
- Variation **> 25%** = release blocker; bisect.

Run-to-run noise sources: kind-cluster CPU pressure from other pods,
host-disk variability for Postgres, port-forward connection churn.

## Releasing a baseline snapshot

After every significant feature merge, run `make k6-baseline` 3x on a
quiet stand and check in the median run's JSON to `results/BASELINE.md`
with a short commentary (build SHA, env description, summary table).
This is the rolling baseline for regression checks.

## ghz (gRPC-direct) jobs

See `ghz/README.md`. Use when you suspect grpc-gateway translation is the
bottleneck or when you want raw-gRPC latency numbers. They mirror the k6
scenarios so the metrics are comparable.

## CI integration

Recommended schedule (workspace CLAUDE.md / load-testing-coach §VIII.1):

| Trigger    | Scenario          |
|------------|-------------------|
| Every PR   | `make k6-dry-run` (syntax only, no stack required) |
| Nightly    | `make k6-smoke && make k6-baseline` |
| Weekly     | `make k6-stress` + `make k6-spike` |
| Pre-release | `make k6-soak` (overnight) |

Thresholds in `scenarios/*.js` are the gating contract — k6 returns non-zero
on breach. No follow-up analyser required for CI gate.

## Anti-patterns we avoid

- One scenario file holding multiple workload classes (latency averaging).
  Each scenario is single-purpose.
- No warm-up phase. `smoke` and `baseline` use `constant-arrival-rate`,
  which preallocates VUs; the first iteration is already warm.
- Hard-coding ids in JS. All ids come from env; if a required one is
  missing the run aborts up-front with a precise message.
- LRO polling with `time.Sleep(1s)`. `poll-op.js` polls at 100ms and
  surfaces parse errors instead of hanging.
