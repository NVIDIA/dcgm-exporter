# Config file fixtures

YAML config files used by the host config scenarios. Each file is named for what it
tests. They are kept in step with the config fixtures in the nv-exporter
repository (the Rust exporter that reads the same YAML files) on a best-effort
basis; there is no sync script, so check both sides when a
config key or rule changes.

## Fixtures

| File | Used by |
| --- | --- |
| `unknown-top-level-key.yaml`, `unsupported-version.yaml` | startup errors and `config validate` |
| `version1-invalid-interval.yaml`, `version1-with-version2-key.yaml` | startup errors |
| `version2-override-overlap.yaml`, `version2-override-matches-nothing.yaml` | startup errors and `config validate`: both report the YAML path of the override once the file is loaded, before DCGM starts; the startup error names no file, `config validate` does |
| `version2-match-invalid-regex.yaml`, `version2-match-group-item.yaml`, `version2-override-invalid-name.yaml`, `version2-enrichment-conflict.yaml` | startup errors and `config validate` |
| `version2-window-on-xid-count.yaml` | `config validate` and `config show` of a window override, and the startup warning when the window is longer than the DCGM history |
| `version2-window-on-normal-field.yaml` | startup errors: a window override on a field that is not a window counter |
| `version2-precedence-settings.yaml` | `config explain` precedence of the listen address, log format and devices |
| `version2-collections.yaml` | `config show`, `validate` and `explain` |
| `version2-file-every.yaml` | cadence through DCGM cache counts, and `configPrecedence` explain, which expects its `every: 2s` |
| `version2-watch-cadence.yaml`, `version2-watch-retention.yaml` | cadence and retention through DCGM cache counts |
| `version2-flag-equivalent.yaml` | file against flags (with `../flag-equivalent-counters.csv`): `config_flag_equivalence_test.go` and `config_deprecated_flags_test.go` |
| `version2-container-enrichment.yaml`, `version2-kubernetes-enrichment.yaml`, `version2-hpc-jobs-enrichment.yaml`, `version2-metrics-file.yaml`, `version2-debug-dump.yaml`, `version2-debug-dump-limits.yaml` | templates whose `${...}` values the test fills in |
| `version1-inline-fields.yaml` | version 1 behavior: `config_inspection_test.go` (`config validate` and `config show`) and `config_serves_metrics_test.go` (served fields) |
