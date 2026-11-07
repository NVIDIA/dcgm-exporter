# Host Integration Tests

Host tests execute a built `dcgm-exporter` product binary and validate behavior
against host DCGM and GPU libraries. The test binary itself is cgo-free; DCGM
linkage lives in the product binary under test.

## Prerequisites

- Linux host with an NVIDIA GPU.
- NVIDIA driver and DCGM installed on the host.
- Go installed at the version pinned by this repository when running from source.
- A built `dcgm-exporter` product binary, passed with `-exporter-binary` for raw
  `go test` runs. The root Taskfile.yml and e2e CLI build and pass this automatically.
- `nv-hostengine` on `PATH`. The cache-count checks of the `configFile`,
  `watchGroups` and `configPrecedence` scenarios start a private one as the test
  user (no root) and fail when it is missing. The `dcgm_uri_integration` suite
  also needs root command access.
- `/dev/vsock` and the `vsock_loopback` kernel module for its VSOCK case only.
- `nv-hostengine` and `dcgmi` on `PATH` for `injectedEventCounts` and
  `gpuLoadUtilization`, which start a private hostengine on a Unix socket as
  the test user. The manual `gpuLoadUtilization` also needs Docker with the
  NVIDIA GPU runtime and `DCGM_IMAGE`, which the e2e CLI sets from
  `--dcgm-image`; without `DCGM_IMAGE` it fails.

## Primary Command

Run from the repository root:

```bash
task test:host
```

Host live scenarios use Ginkgo labels. For focused iteration, run the package
directly with a label filter:

```bash
go build -o /tmp/dcgm-exporter ./cmd/dcgm-exporter
go test ./tests/host -v -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=startupMetrics
go test ./tests/host -v -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=dcgmLibraryPath
go test ./tests/host -v -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter='startupTLS || reload'
```

`startupMetrics`, `defaultPreset`, `ipv6Listen`, `systemdSocket` and
`gpuBindUnbindWatch` require the output to be exactly the CSV the exporter runs
with. They wait until every row `E2E_DCGM_FIELD_SUPPORT` lists as supported is
emitted. A CSV row the exporter does not emit fails only when that variable
lists it as supported; otherwise it is reported as a skip with the reason. The e2e CLI sets that
variable from `dcgmi dmon`; for a direct run, set it yourself (format in
[the E2E architecture guide](../../docs/architecture/e2e/README.md#output-equals-the-runs-csv)).

The direct DCGM URI scenarios are behind the `dcgm_uri_integration` build tag
because they need `nv-hostengine` and root command access. TCP and Unix do not
need VSOCK:

```bash
E2E_REQUIRE_DCGM=1 go test --tags=dcgm_uri_integration ./tests/host -v \
  -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=dcgmUri

E2E_REQUIRE_DCGM=1 go test --tags=dcgm_uri_integration ./tests/host -v \
  -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=dcgmUriUnix
```

For VSOCK, load the loopback module and require it explicitly:

```bash
sudo modprobe vsock_loopback
E2E_REQUIRE_VSOCK=1 E2E_REQUIRE_DCGM=1 go test --tags=dcgm_uri_integration ./tests/host -v \
  -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=dcgmUriVSOCK
```

The e2e CLI selects ordinary host scenarios and direct URI coverage through
Ginkgo labels such as
`startupMetrics`, `configFile`, `configPrecedence`, `watchGroups`, `configInspection`, `configStartupErrors`, `configEnrichment`, `configServesMetrics`, `hpcJobMapping`, `startupTLS`, `reload`, `profilingReload`, `gpuLinkFields`, `ipv6Listen`,
`latestValueBatching`, `oversizedFieldGroup`, `systemdSocket`, `gpuSelection`, `modelNameLabels`, `scrapeLimit`, `httpTimeouts`, `logLevels`, `injectedEventCounts`,
`gpuLoadUtilization`, `gpuBindUnbindEvents`, `migGpuLevelFields`, and `dcgmUri`.
`profilingReload` needs `CAP_SYS_ADMIN` for its DCGM profiling field watches;
the other `reload` specs can run without it. The CLI prepares a private
hostengine for its profiling probe with `--bootstrap=install`; `check` and
`skip` require a prepared DCGM service. After a supported probe, the CLI sets
`E2E_REQUIRE_PROFILING=1`, so missing profiling metrics fail the run. Direct
native profiling runs require that variable as an assertion that the host was
prepared and profiling support was checked. Without it, those two optional
specs skip before starting the exporter. `host/defaultPreset` runs the
shipped counter preset and may need root for DCGM field watches. Select it
explicitly on a privileged host; GPU CI runs `host/startupMetrics` with the
smaller test counter fixture.

The manual `host/gpuLinkFields` scenario asks DCGM which numeric fields have
samples on an active GPU-owned NVLink. It then checks that the packaged exporter
publishes those fields with the same GPU UUID and physical link index. This
includes NVLink transmit and receive fields when DCGM provides them. It checks
field presence, type, labels, and finite values. Live samples can change between
the direct read and the scrape, so it does not compare their numeric values.
This proof targets the GPU selected by the probe; switch-owned links are covered
separately.

The scenario needs a private standalone DCGM hostengine, an active GPU-owned
link, any required profiling privilege, and the matching `e2e-dcgm-probe`
binary and go-dcgm field source.

Select this scenario explicitly. The E2E CLI resolves the probe binary and
field source; the private hostengine must already be running and available to
both the probe and exporter:

```bash
task build:e2e
E2E_DCGM_HOSTENGINE_ADDRESS=127.0.0.1:5556 ./bin/e2e tests \
  --suite host --scenario host/gpuLinkFields --bootstrap=skip
```

For a direct Go test run, provide the packaged binaries and field source:

```bash
E2E_DCGM_HOSTENGINE_ADDRESS=127.0.0.1:5556 go test ./tests/host -v \
  -run '^TestHostIntegration$' -args \
  -exporter-binary=/path/to/packaged/dcgm-exporter \
  -dcgm-probe-binary=/path/to/e2e-dcgm-probe \
  -dcgm-fields-file=/path/to/go-dcgm/pkg/dcgm/const_fields.go \
  --ginkgo.label-filter=gpuLinkFields
```

If the probe does not finish in 60 seconds, the test fails with "DCGM did not
respond within 60s during <stage>", naming the last probe step it started, or
"GPU-link probe did not finish within 60s before its first DCGM call".
You start the hostengine for this manual scenario, so stop it when you finish,
and restart it after a hang.

The probe creates temporary DCGM groups and watches, then removes them. The
scenario fails when there is no active GPU-owned link or no readable numeric
field, and it does not pin a DCGM version.

For native runs, set `E2E_SUITE_RESULT_MARKERS=true` to emit `&&&&` markers for
each executed spec. For E2E CLI runs, pass `--result-markers`;
`--no-result-markers` disables both the host suite lifecycle and per-spec
markers. Runtime Ginkgo skips are reported as `WAIVED`.

Expect startup and some scrape checks to take at least one DCGM collection interval. By default, dcgm-exporter uses a 30-second polling interval, so tests wait for metrics instead of relying on fixed short sleeps.

## Injected events and real load

`host/injectedEventCounts` uses `dcgmi test --inject` to write an XID
(`DCGM_FI_DEV_XID_ERRORS`) and a clock-event reason
(`DCGM_FI_DEV_CLOCKS_EVENT_REASONS`) into the private hostengine's cache for the
GPU with the highest ID. Injection changes only DCGM's cache, not the GPU. The
exporter runs with 15-second count windows. The scenario picks an XID and a
reason that no GPU reports before the injection, then checks that only that
GPU's `DCGM_EXP_XID_ERRORS_COUNT{xid}`, `DCGM_EXP_XID_ERRORS_TOTAL{xid}`,
`DCGM_EXP_CLOCK_EVENTS_COUNT{clock_event}` and
`DCGM_EXP_CLOCK_EVENTS_TOTAL{clock_event}` become positive, that the counts
carry `window_size_in_ms="15000"`, and that its `DCGM_FI_DEV_XID_ERROR` equals
the XID. Three seconds before the window ends the counts are still positive;
within 10 seconds after it they are zero again, and the totals keep the events.

A second case in the same scenario runs from a config file whose metrics
override gives `DCGM_EXP_XID_ERRORS_COUNT` a 10-second window and sets no
`--xid-count-window-size` (an explicit flag takes precedence over the file's
override). After an injection the XID count carries `window_size_in_ms="10000"`
and returns to zero within 25 seconds (the 10-second window plus 15 seconds for collection and scraping), far before the 5-minute default.

`host/gpuLoadUtilization` is manual: name it with `--scenario`. It needs Docker
with the NVIDIA GPU runtime and the DCGM image from `--dcgm-image`. It chooses
the GPUs that `nvidia-smi` shows with MIG off, no compute process and `utilization.gpu`
below 20%, and skips when there are fewer than two. The exporter must have one
`DCGM_FI_DEV_GPU_UTIL_RATIO` sample for each of them. It picks
`dcgmproftester13` or `dcgmproftester12` from the CUDA version `nvidia-smi`
reports (and skips for older CUDA), checks the image has it, and runs it with
`--no-dcgm-validation --target-max-value -t 1001` on the highest-ID idle GPU
with `docker run --gpus device=<uuid>`. For 20 seconds of load, every scrape
must show the loaded GPU's utilization above 0.5 and every other idle GPU's
below 0.2. Over that time the loaded GPU must use at least 1.2 times the energy
of the busiest other idle GPU (`DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL`). The
container is then removed. `DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO` gets the same
utilization checks when `dcgmi profile --list` lists graphics engine activity;
otherwise that check is skipped and logged.

`host/migGpuLevelFields` is manual: name it with `--scenario`. The e2e CLI puts
every GPU in MIG mode with two instances of the largest profile that fits
twice, sets `E2E_MIG_LAYOUT=two-instances-per-gpu`, and restores MIG
afterward; without that variable the spec skips. The exporter runs with the default CSV
and flex device selection, and is scraped every 5 seconds for 65 seconds.
Every scrape must succeed with the exporter running, show two GPU instances on
every GPU that `nvidia-smi` lists, and pass
`metriccontract.ValidateMIGSeriesShape`.

`host/gpuBindUnbindEvents` is manual: name it with `--scenario`. The e2e CLI
then sets `E2E_ALLOW_GPU_UNBIND=1`; without it the spec skips, so a plain
`go test ./tests/host` never unbinds a GPU. It starts the exporter with
`--enable-gpu-bind-unbind-watch --gpu-bind-unbind-poll-interval=2s`, then
unbinds one GPU from the `nvidia` driver by writing its PCI address to
`/sys/bus/pci/drivers/nvidia/unbind` (as root, or through `sudo -n`, so sudo
must not ask for a password). The GPU is the highest-index one with MIG off and
no compute process. Its `DCGM_FI_DEV_GPU_UTIL_RATIO` series must disappear
within 32 seconds (one 2-second poll plus 30 seconds for the exporter to
reload), every other GPU with MIG off must keep its series, and the exporter
log must show at least one more `GPU lifecycle event detected` and at least one
more `GPU metrics resumed after lifecycle reset` line. The scenario then writes
the address to `.../bind`, and the same must hold within the same time.

Before unbinding, the scenario fails (it does not skip) unless: the host has at
least two GPUs and one is idle with MIG off; the driver is release 590 or later
(older drivers can hang an unbind while the GPU is open); persistence mode is
off and `nvidia-persistenced` is not active; no other process holds the GPU's
`/dev/nvidia<minor>` file (checked with `sudo -n fuser`); and `nvidia-fabricmanager`
is not active, `nvidia-smi -q` shows no fabric or cluster UUID, and no NVLink is
active (unbinding a member of a fabric disturbs it). The sysfs write is stopped
after 60 seconds. After a failed unbind the scenario waits up to 30 seconds for
the GPU to leave the driver; if it is still bound, the failure says so and
names the recovery steps. The GPU is bound again on every exit path of the test;
if that fails, the message gives the command to run by hand. A killed test
process cannot rebind, so check `nvidia-smi` afterwards.

## MIG XID counters

The `host/migXidCounters` scenario checks XID COUNT/TOTAL values and labels for
one MIG compute instance. It injects known samples into fake DCGM entities and
compares two exporter scrapes with DCGM history, checking that new samples are
counted once.

Run as root with an installed NVML injection library and a YAML file matching
the installed DCGM version:

```bash
sudo ./bin/e2e tests --suite host --scenario host/migXidCounters \
  --bootstrap=skip --dcgm-nvml-injection-yaml=/path/to/H200-With-MIG.yaml
```

The test runs exporter code compiled into `e2e-dcgm-probe`. Physical MIG discovery
and driver-generated XIDs need separate hardware tests. See the
[probe guide](../cmd/e2e-dcgm-probe/README.md) for sample values and report details.

## File Layout

- `startup_metrics_test.go`: direct startup, metrics scrape, YAML config file, and HPC job mapping behavior.
- `hpc_job_mapping_safety_test.go`: HPC job mapping refuses a symlinked directory, a symlinked or hard-linked file and a file over the size limit; GPU samples stay without job labels and the exporter warns once.
- `gpu_inventory_test.go`: compare the identity labels and values of `startupMetrics` and `defaultPreset` with `nvidia-smi` on the host, retrying until they agree or 60 seconds pass.
- `startup_tls_test.go`: TLS and basic-auth behavior when running on the host.
- `ipv6_listen_test.go`: direct IPv6 loopback listen and metrics scrape.
- `systemd_socket_test.go`: inherited systemd socket activation listener behavior.
- `reload_test.go`: SIGHUP and file-watcher reload behavior.
- `mig_xid_counters_test.go`: MIG compute-instance XID counts and labels.
- `gpu_link_fields_test.go`: check available GPU-owned link fields against direct DCGM field and physical-link identity.
- `config_startup_errors_test.go`: bad `--config-file`, flag or environment input must stop the exporter with exit status 1 and a message that names the YAML path, flag or variable, before DCGM starts. Needs no GPU.
- `config_inspection_test.go`: the `config show`, `config validate` and `config explain` commands on the built binary, including which of a file, an environment variable and a flag wins for the listen address, log format and devices. Needs no GPU or DCGM.
- `config_deprecated_flags_test.go`: one warning for the deprecated `--configmap-data` flag in `config validate`, and no warning for a flag with no config key in `config validate` or at startup.
- `config_watch_cadence_test.go`: file `every`, override `every`, sample limits and the flag, environment and file precedence, read from DCGM cache counts through a private `nv-hostengine`. `config_precedence_test.go` also checks `config explain` for the same inputs.
- `config_helpers_test.go`: helpers for the config command and fixture checks.
- `config_flag_equivalence_test.go`: a version 2 file and the equivalent flags serve the same families.
- `gpu_selection_test.go`: `gpuSelection` runs a baseline exporter, then selects GPUs with `-d g:...` and with `metrics[].entities.gpu`, and checks every family keeps only the selected GPUs' series; it skips with fewer than two GPUs. The unit tests in the file need no GPU.
- `model_name_labels_test.go`: `modelNameLabels` checks every model name label against the `nvidia-smi` name with the default and with `--replace-blanks-in-model-name`; it skips when no GPU name has a blank. It is expected to fail on hosts where DCGM serves GPU NVLink link series (NVLink 5 and later), until the exporter applies the flag to those series too: `internal/pkg/collector/gpu_collector.go` builds their `model_name` without it. When `nvidia-smi` shows an active NVLink but DCGM serves no link series, it logs that the link part was not checked; with no active NVLink it requests no link series.
- `scrape_limit_test.go`: `scrapeLimit` sets `--max-concurrent-scrapes` and `server.max_concurrent_scrapes` to 1 and checks overlapping scrapes get 503.
- `http_timeouts_test.go`: `httpTimeouts` checks the read timeout (`--web-read-timeout`, `server.read_timeout`) and the write timeout (`--web-write-timeout`, `server.write_timeout`). With a read timeout of 2s a connection that stalls inside a request line is closed within 5s; with the default it is still open after 5s. With a write timeout of 1ms the scrape fails and the exporter logs `Failed to write response.`; with the default the scrape succeeds and nothing is logged. The helpers are tested without a GPU.
- `log_levels_test.go`: `logLevels` checks that debug records (`level=DEBUG`) appear only with `--debug` or `observability.logs.level: debug`, and that DCGM log lines (the exporter routes the embedded DCGM log through its own logger and adds a `dcgm_level` field carrying the DCGM level, `dcgm_level=DEBUG` in text and `"dcgm_level":"DEBUG"` in JSON) appear in the process output only with `--enable-dcgm-log` or `sources.dcgm.embedded_log`; `--dcgm-log-level DEBUG` must produce at least one INFO, DEBUG or VERB line, and `ERROR` may produce only FATAL or ERROR lines. An ERROR run with no DCGM line also passes, so it proves only that nothing below ERROR appears. The scenario uses the embedded engine.
- `config_inline_field_text_test.go`: checks the `# TYPE` and `# HELP` lines of the inline field that `configFile` serves.
- `injected_event_window_test.go`: the override window case of `injectedEventCounts`.
- `startup_metrics_test.go`, `startup_tls_test.go` and `ipv6_listen_test.go` also compare the exporter metrics (`--enable-exporter-metrics`, `observability.metrics.enabled`), the web config file (`--web-config-file`, `server.web_config_file`) and the listen address (`--address`, `outputs.prometheus.listen`, on IPv4 and IPv6) with the baseline.

Each of these scenarios compares a baseline exporter run with a run per flag form and key form, using the shared helpers in `helpers_test.go`, and checks the flag and key forms serve the same families, series and label names.
- `config_enrichment_test.go`: container enrichment `auto`, `true` and `false` against a fake Docker-compatible socket (a required source that never appears waits 60 seconds, so that one case takes about a minute; another starts the socket late), Kubernetes enrichment `auto` with a missing kubelet socket, and HPC job enrichment `true` (the mapping directory appears late) and `false`. The log proves a required source made startup wait.
- `config_count_window_test.go`: a running exporter logs one warning, naming the override and the flag that set the retention, when a count window is longer than the DCGM history, and still starts.
- `debug_dump_test.go`: a version 2 file with `debug_dump.enabled: true` writes metrics and device-information JSON dumps (mode 0600) to a directory it creates (mode 0700); refuses a dump directory that is a symbolic link, logs it and keeps serving metrics; and keeps the directory within `max_files`, then within `max_total_bytes` sized from the largest dump seen, across scrapes.
- `reload_config_file_test.go`: what SIGHUP and counters file changes do with a version 2 file.
- `config_serves_metrics_test.go`: a version 1 config file serves only its listed fields under NVML injection. Select it with `--dcgm-nvml-injection-yaml`; needs no GPU.
- `testdata/config/`: YAML config fixtures named for what they test, kept in step with the nv-exporter repository on a best-effort basis.
- `suite_test.go`: Ginkgo suite registration for ordinary host scenarios.
- `remote_dcgm_uri_suite_test.go`: Ginkgo suite registration for the `dcgm_uri_integration` build-tagged scenario.
- `remote_dcgm_uri_test.go`: opt-in live remote DCGM URI checks behind the `dcgm_uri_integration` build tag.
- `helpers_test.go`: shared host test helpers.
- `injected_events_test.go`: XID and clock-event counts after an injection into one real GPU.
- `gpu_load_test.go`: utilization and energy under `dcgmproftester` load on one GPU against the other idle GPUs.
- `gpu_samples_test.go`: per-GPU sample sums and values read from one scrape.
- `private_hostengine_test.go`: a private `nv-hostengine` on a loopback port or a Unix socket, and `dcgmi` against it.

## Guidance

- Keep host-specific setup, process management, YAML config, HPC/SLURM-style job mapping, and signal behavior in this directory.
- Move reusable scrape or metric assertions into `tests/internal` when they can also be used by container or Kubernetes tests.
- Skip an optional test only when an independent check proves the feature unsupported; a missing prerequisite or unknown support fails or is reported as not run, never as a skip. For example, a missing `nv-hostengine` fails the cache-count checks.
