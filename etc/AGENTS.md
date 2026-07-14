# etc - Agent Instructions

`etc/` contains shipped metric CSV files. These files are user-facing contracts.

## Metric CSV Contract

- Active metric rows use exactly:
  `DCGM FIELD, Prometheus metric type, help message`
- Supported Prometheus types are the types accepted by
  `internal/pkg/counters` and rendered by `internal/pkg/rendermetrics`.
- Do not invent DCGM field names or exporter-owned names. Verify from
  go-dcgm/DCGM bindings or `internal/pkg/counters`.
- Commented rows are documentation and examples, but tests may still assert
  that important optional rows remain documented.
- Custom metrics are complete replacements, not additive overlays.

## Validation

Run at least:

```bash
go test ./internal/pkg/counters
make test-main
```

If version examples or derived files change, also run `make validate-versions`.
