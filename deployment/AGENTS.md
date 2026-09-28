# deployment - Agent Instructions

`deployment/` contains the Helm chart and Kubernetes-facing configuration.

## Helm And Kubernetes

- Keep `deployment/values.yaml`, templates, raw YAML examples, README snippets,
  and CLI/env behavior aligned.
- `arguments` pass directly to dcgm-exporter. Use real flag names from
  `pkg/cmd/app.go`.
- Service read/write timeout values and ServiceMonitor scrape timeout/interval
  should remain coherent.
- RBAC changes must match the Kubernetes features they enable, such as pod
  labels, pod UID, virtual GPU mapping, or DRA.
- Security context changes are user-visible. Preserve the documented
  SYS_ADMIN/profiling tradeoff unless deliberately changing it with tests and
  docs.
- TLS and basic auth should use exporter-toolkit web config generation and
  mounted secrets, not custom ad hoc handling.

## Validation

Run Helm/template/unit tests relevant to the change. For broad chart changes,
also run Kubernetes e2e tests when a suitable GPU cluster is available, or
record the prerequisite gap in the MR.
