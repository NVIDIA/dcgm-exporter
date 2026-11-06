# pkg/cmd - Agent Instructions

The root `AGENTS.md` still applies. This file adds rules for `pkg/cmd/`.

`pkg/cmd/app.go` owns the executable contract: CLI flags, environment variables,
startup, logging configuration, web configuration, runtime reload wiring, and
optional pprof endpoints.

This package holds only code that needs the CLI framework or the flag
definitions. The rest of the configuration code lives in other packages:

- `internal/pkg/appconfig` owns the defaults (`DefaultConfig`), the flag-name
  strings (`Flag...` constants, which the exported `CLI...` constants here
  alias), `ValidateValues` and `ValidateCombinations`, the settings table, and
  the resolved value (`appconfig.Resolved`) that startup and inspection share.
- `internal/pkg/configinspect` owns the `config show`, `explain` and `validate`
  reports. It returns bytes and never writes to a terminal.
- `internal/pkg/devicewatchlistmanager` owns `CheckOverrides`, which startup
  (through `CheckOverridesInLocalSource`, on the metric source it reads once)
  and `config validate` call, each adding its own context. It also owns the warning that a count window is longer
  than the DCGM history. `counters.ReadLocalRecords` reads a file or inline
  metric source for both and for the registry build.

What stays here: the `config` subcommands, their options and writing their
results (`config_command.go`), flags into `Config` and the deprecated-flag
table (`flags.go`), the device selector syntax (`device_options.go`), flag and
environment origin tracking (`flag_origins.go`),
and layering that builds the resolved value plus the startup messages for
enrichment sources (`resolve.go`).

## CLI And Environment

- Add or change flags only in `NewApp`, and keep names, aliases, defaults,
  usage text, and environment variables aligned. A new flag needs its name in
  `appconfig` (`Flag...`), a row in the settings table in
  `internal/pkg/appconfig/settings.go`, and a `CLI...` alias here.
- Reflect user-visible flag changes in README, Helm values/templates when
  applicable, and tests.
- Keep remote DCGM host engine formats aligned with supported DCGM connection strings:
  `<HOST>:<PORT>`, `tcp://<HOST>:<PORT>`, `unix:///<SOCKET_PATH>`,
  `vsock://<CID>:<PORT>`, and bracketed IPv6 host:port values.

## Runtime Behavior

- Treat startup validation and reload paths as operationally sensitive. Preserve
  clear errors and logs for bad flags, malformed duration values, missing DCGM,
  and invalid web config.
- Cumulative exporter counters are in-memory state. If reload or runtime swap
  behavior changes, document reset or preservation semantics and add tests.
- `--web-config-file` follows Prometheus exporter-toolkit behavior. Do not
  reimplement TLS or basic auth parsing in this package.

## Tests

- Add focused CLI/app tests for new defaults, aliases, env var behavior, invalid
  values, and runtime setup branches. Tests that drive the real CLI app stay
  here; a rule about configuration values or a report is tested in the package
  that owns it.
- Do not require GPU hardware for pure CLI parsing tests.
