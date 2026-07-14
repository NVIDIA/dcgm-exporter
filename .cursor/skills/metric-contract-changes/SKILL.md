---
name: metric-contract-changes
description: Use when changing metric CSV files, exporter-owned counters, Prometheus labels, or metric rendering.
---

# Metric Contract Changes

1. Read `llms.txt`, `etc/AGENTS.md`, and `internal/pkg/AGENTS.md`.
2. Identify the owning code path in `internal/pkg/counters`,
   `internal/pkg/collector`, or `internal/pkg/rendermetrics`.
3. Update code, CSV, docs, and Helm examples together when behavior changes.
4. Run:

```bash
go test ./internal/pkg/counters
make test-main
```

5. Add GPU/Docker/Kubernetes validation only when the change needs that layer.
