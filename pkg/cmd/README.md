# pkg/cmd

Importable CLI and application wiring for dcgm-exporter. The thin `package main`
entrypoint lives in [`cmd/dcgm-exporter`](../../cmd/dcgm-exporter). See
[AGENTS.md](AGENTS.md) for change guidance.

This package owns only what needs the CLI framework: flag definitions, layering
flags and environment variables into a config, startup, reload wiring, and
writing the output of `dcgm-exporter config show|validate|explain`.

## Where the rest lives

- Defaults, value checks and the resolved config:
  [`internal/pkg/appconfig`](../../internal/pkg/appconfig).
- The `config show`, `validate` and `explain` reports:
  [`internal/pkg/configinspect`](../../internal/pkg/configinspect).
- The override check shared by startup, `config validate` and the registry:
  [`internal/pkg/devicewatchlistmanager`](../../internal/pkg/devicewatchlistmanager).

## Where the CLI contract is pinned

- Help text: the `help-*.txt` goldens in [`testdata`](testdata).
- Inspection output: the `show-*`, `explain*` and validate goldens in
  [`testdata`](testdata).
- Released flags: names, aliases, environment variables and defaults in
  [`testdata/released-flags.txt`](testdata/released-flags.txt).
- Exit status 0 (success), 1 (any other failure, including an invalid
  configuration) and 2 (wrong command usage):
  [`exit_status.go`](exit_status.go).

## Behavior

For what the flags, environment variables, config file and inspection commands
do, see the
[command reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html)
and `dcgm-exporter --help`. Update the goldens in the same change as any
behavior change.
