# Signal handling tests

Black-box behavioural tests that prove `dcgm-exporter` honours `SIGTERM`, `SIGINT`, and `SIGHUP` correctly. Run the real binary, send real signals, scrape real `/metrics`. See `docs/CONTEXTS.md` for the context-propagation model the tests exercise.

## What's tested

| Test | Signal | Expectation |
| --- | --- | --- |
| 1 | `SIGTERM` | Daemon exits within `SHUTDOWN_BUDGET_SECONDS` (default 5 s). |
| 2 | `SIGINT` | Daemon exits within budget. |
| 3 | `SIGHUP` | Daemon performs hot reload, stays running, `/metrics` keeps responding. |
| 4 | 3 × `SIGHUP` | Daemon survives all three reloads, `/metrics` still responds. |

## Requirements

- `libdcgm.so.4` and `libnvidia-ml.so.1` discoverable by the dynamic linker (NVIDIA DCGM installed, or running inside an `nvidia-container-runtime` container).
- `curl` on `PATH`.
- The `dcgm-exporter` binary at `cmd/dcgm-exporter/dcgm-exporter` (produced by `make binary`).

### NixOS

The script auto-detects NixOS (`/etc/NIXOS`) and prepends the standard NixOS library locations to `LD_LIBRARY_PATH`:

- `/run/opengl-driver/lib` for `libnvidia-ml.so.1` (NVIDIA driver, NixOS convention).
- The first `libdcgm.so.4` found under `/nix/store/*/lib` (e.g. the `pkgs.dcgm` package).

Any pre-existing `LD_LIBRARY_PATH` entries are preserved. To pin a specific DCGM version, export `LD_LIBRARY_PATH` yourself before running — the auto-detection only prepends paths for libs it cannot already find.

If `PORT` (default `9499`) is already bound (e.g. by a running `dcgm-exporter.service`), the script auto-increments up to 20 ports and runs on the first free one.

## Running

From the repo root:

```bash
make test-signals
```

Or directly (after `make binary`):

```bash
./tests/signals/test_signals.sh
```

## Environment overrides

| Var | Default | Purpose |
| --- | --- | --- |
| `BIN` | `cmd/dcgm-exporter/dcgm-exporter` | Path to the daemon binary. |
| `PORT` | `9499` | HTTP port to bind. Off the canonical 9400 to avoid clashing with a running production exporter. |
| `COUNTERS` | `etc/default-counters.csv` | Counters CSV. |
| `SHUTDOWN_BUDGET_SECONDS` | `5` | Max time allowed between signal and exit. |
| `STARTUP_TIMEOUT_SECONDS` | `30` | Max time waiting for `/metrics` to respond. |
