# ghz direct-gRPC jobs

These Kubernetes Jobs hit `kacho-nlb` directly on its gRPC port (9090),
bypassing api-gateway's grpc-gateway translation layer. Use them when k6
results show api-gateway as the bottleneck OR for comparing grpc-gateway
overhead vs raw gRPC.

## Prereqs

1. ghz image built and loaded into the cluster (KIND example):
   ```bash
   docker build -t ghz:dev .
   kind load docker-image ghz:dev --name kacho
   ```
2. Loadbalancer service reachable as `nlb.kacho.svc.cluster.local:9090`.
3. Proto descriptor set baked into the image OR available via reflection.
   `kacho-nlb` enables grpc-reflection by default in dev, so the jobs use
   reflection (no protoset needed).

## Jobs (mirror the k6 scenarios)

| File | Scenario | Cardinality |
|------|----------|-------------|
| `smoke-job.yaml`    | smoke    | 3 000 calls / 100 cps |
| `baseline-job.yaml` | baseline | 150 000 calls / 500 cps |
| `stress-job.yaml`   | stress   | ramp via repeated runs |
| `spike-job.yaml`    | spike    | step-up via 2 sequential jobs |

`soak` is intentionally omitted — for 60min runs, the k6 cluster-side
scenario is preferred (richer per-window analysis).

## Apply

```bash
kubectl apply -f baseline-job.yaml
kubectl -n kacho wait --for=condition=complete job/ghz-nlb-baseline --timeout=600s
kubectl -n kacho logs -l job-name=ghz-nlb-baseline
```
