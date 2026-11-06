# deployment - Agent Instructions

The root `AGENTS.md` still applies. This file adds rules for `deployment/`.

`deployment/` contains the Helm chart and Kubernetes-facing configuration.

## Helm And Kubernetes

- Keep `deployment/values.yaml`, templates, raw YAML examples, README snippets,
  and CLI/env behavior aligned.
- `arguments` pass directly to dcgm-exporter. Use real flag names from
  `pkg/cmd/app.go`.
- Exporter settings are set in the chart's `config` value (version 2,
  snake_case). Chart values configure Kubernetes objects; a template that needs
  an exporter setting reads it through `dcgm-exporter.config`. Value checks live
  in `dcgm-exporter.validateValues`, which daemonset.yaml includes once. A
  removed chart value fails rendering with its replacement named
  (`dcgm-exporter.rejectRemovedValues`). `arguments` and `extraEnv` override
  `config`. While the chart renders `config`,
  `dcgm-exporter.rejectSettingOverrides` rejects flags and env vars for settings
  its objects or checks rely on. With `configMap.existingName` or
  `config: null` the chart skips the checks that read `config`; NOTES.txt prints
  the metrics path when `customMetrics` is set and, with `existingName`, the web
  config path. Checks cover values the chart owns; do not parse
  `arguments` or `extraEnv` values to predict what the exporter reads.
- `config.server.write_timeout` and the ServiceMonitor scrape timeout/interval
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
