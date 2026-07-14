# pkg/cmd - Agent Instructions

`pkg/cmd/app.go` owns the executable contract: CLI flags, environment variables,
defaults, startup validation, logging configuration, web configuration, runtime
reload wiring, and optional pprof endpoints.

## CLI And Environment

- Add or change flags only in `NewApp`, and keep names, aliases, defaults,
  usage text, and environment variables aligned.
- Reflect user-visible flag changes in README, Helm values/templates when
  applicable, and tests.
- Keep remote hostengine formats aligned with supported DCGM connection strings:
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
  values, and runtime setup branches.
- Do not require GPU hardware for pure CLI parsing tests.
