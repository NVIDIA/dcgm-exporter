# E2E architecture

Tests are ordinary Go tests and Ginkgo suites under `tests/`. The `e2e` CLI
picks scenarios, prepares what they need, runs them, and reports results. Tests
also run directly with `go test` once their prerequisites exist:

```sh
go test ./tests/static -run '^TestChartKubernetesRBACRenderContract$'
```

## Layout

```text
cmd/e2e/                        # CLI entry point
internal/e2e/
  cli/ config/                  # Flags, environment, coordination
  scenario/catalog.go           # Scenario definitions
  capability/                   # Hardware and cluster probes
  bootstrap/ cluster/ mig/      # Dependencies, test cluster, MIG setup and restore
  suite/ exec/                  # Build and run test binaries
tests/
  static/ host/ container/ k8s/ # Suites
  internal/                     # Shared test helpers
```

## Scenario catalog

`internal/e2e/scenario/catalog.go` is the only scenario list. Each entry maps a
`suite/name` selector to a Go test name or Ginkgo label, plus its group and
enable mode. CI uses the same catalog.

```go
{
	Suite:       SuiteStatic,
	Name:        "chartRbacRendering",
	Group:       GroupStaticBaseline,
	Description: "Helm chart RBAC rendering for Kubernetes attribution features",
	ResultName:  "TestChartKubernetesRBACRenderContract",
	Enabled:     "always",
},
```

This maps `static/chartRbacRendering` to the Go test in the first example.

A scenario's `Enabled` value sets when it runs:

| `Enabled` | Runs |
| --- | --- |
| `always` | In a default run, unless a capability probe finds the hardware missing. |
| `manual`, `nvml_injection`, `nvsdm_mock` | Only when named with `--scenario`. |
| `gpu_operator`, `shared_gpu` | In a default run: `--gpu-operator` defaults to `install` and `--shared-gpu-configure` to `true`, and `auto` or `existing` (`--gpu-operator`) and `auto` (`--shared-gpu-configure`) also enable them. Only `false` leaves them out unless named. |
| `gpu_operator_chart` | When `--gpu-operator=install` and `--gpu-operator-chart <path>` are set (a local GPU Operator chart, usually its main branch), or when named. |

## Timeout, log-level, selection and GPU bind/unbind scenarios

`host/httpTimeouts` closes a stalled connection with `--web-read-timeout` and
`server.read_timeout`, and fails the scrape with `--web-write-timeout` and
`server.write_timeout`. `host/logLevels` checks that debug records and the
embedded DCGM log reach the process output only when `--debug`,
`--enable-dcgm-log` or their config keys ask for them. The check
recognizes DCGM log lines by the `dcgm_level` field the exporter adds when it
routes the embedded DCGM log through its own logger, and takes the DCGM level
from it; this was confirmed on a B200.

The Kubernetes hardware scenarios add selected-subset steps after their
default run. The steps run inside the existing scenarios, so which scenarios
the planner chooses for a host does not change. Each step takes the entity IDs
from the default run's scrape (once two scrapes in a row report the same
entities), installs the exporter again with a flag selecting the highest ones,
and requires samples for exactly the selected entities: every selected one
present and no other. The MIG step differs: it reads an all-instances run and
selects the instance the planner passes, not the highest. The config-key forms
select the same entities and are covered by the flag steps; the
key-to-selector mapping is proved by the `internal/pkg/appconfig` tests.

| Scenario | Selection | Checked |
| --- | --- | --- |
| `k8s/nvswitch` | `--switch-devices=g:<switch>` | `DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT` for that switch only |
| `k8s/nvswitchThroughput` | `--switch-devices=g:<switch>+i:<link>,<link>` | `DCGM_FI_DEV_NVSWITCH_LINK_THROUGHPUT_TX` for that switch and those two links only |
| `k8s/graceCpu` | `--cpu-devices=g:<cpu>+i` | `DCGM_FI_DEV_CPU_UTIL_TOTAL` for that CPU only |
| `k8s/graceCpu` | `--cpu-devices=g:<cpu>+i:<core>,<core>` | `DCGM_FI_DEV_CPU_UTIL_TOTAL` for those two cores of that CPU only |
| `k8s/migSpecificInstanceSelection` | `--devices i:<instance>` (as `DCGM_EXPORTER_DEVICES_STR`) | exactly one GPU instance, with that instance's `GPU_I_ID` (the ID repeats on every MIG GPU, so the GPU is not named) |

The exporter prints a switch as `nvswitch<N>`; links, CPUs, cores and GPU
instances are plain numbers.

`host/gpuBindUnbindEvents` (manual) unbinds one idle GPU from the `nvidia`
driver and binds it again with the bind/unbind watch on. It checks that the
GPU's series disappear and return while every other GPU keeps reporting, and
that the exporter logs each lifecycle event and recovery. It fails before
unbinding on a host where the unbind could hang or disturb a fabric, and
rebinds on every exit path. See the [host tests guide](../../../tests/host/README.md).

## CLI

```text
e2e tests    # select, prepare, run
e2e cluster  # check | up | deploy | deploy-dcgm | status | logs | cleanup
```

Useful `e2e tests` options (see `e2e tests --help` for all):

| Option | Use |
| --- | --- |
| `--list-scenarios` | List selectors. Add `--verbose` for the Go command behind each. |
| `--dry-run` | Show the plan without probing, building or running. |
| `--suite`, `--scenario`, `--skip-suite`, `--skip-scenario` | Select tests. Repeat the flag or set the `E2E_*` variable. |
| `--bootstrap-only`, `--build-only` | Stop after dependency setup, or after building test binaries. |
| `--result-markers` | Print stable `&&&&` result lines for CI. |

Exit status 10 means setup failed; 20 means a test failed.

Each test group in the host, container and k8s suites writes a Ginkgo JSON
report to a temporary file. A failed group lists each failed spec with its
full name, `file:line` and summarized failure message. When the report is
missing or unreadable, the failure says "no Ginkgo report" and shows the exit
status and the last 20 stderr lines, labeled as stderr. When the report lists
no failed spec, the failure says so and shows the same exit status and stderr
tail. At the end of the run, the specs that called `Skip` are listed under
their group, with their reasons, whether or not the group failed. Result
markers are unchanged.

Flags override environment variables, which override defaults:

```sh
export E2E_RUN_NAME=example            # names the resources this run owns
export E2E_KUBECONFIG=/path/to/kubeconfig   # use an existing cluster
```

Without a kubeconfig, the CLI creates a local k3d cluster. For runnable
commands, see the [tests guide](../../../tests/README.md).

## Execution environments

Use the cheapest environment that proves the behavior.

| Environment | Suites and groups | Needs | CI job and task |
| --- | --- | --- | --- |
| CPU only | `static`; `host/configStartupErrors`, `host/configInspection` | The exporter binary (host scenarios) | `test-e2e-bundle-smoke` (`task test:e2e:smoke`, `static/paths` only), `test-e2e-dcgm-mocks` (`task test:e2e:mocks`, the two host scenarios) |
| CPU with DCGM injection or mocks | `host-nvml-injection`, `host-config-injection`, `host-nvsdm-mock` | A DCGM fixture YAML | `test-e2e-dcgm-mocks` (`task test:e2e:mocks`) |
| Linux host with GPU | `host-*` | GPU, driver, DCGM libraries | `test-e2e-host` (`task test:e2e:host`) runs a named list of host scenarios; the others, such as `host/gpuSelection`, run in a full default run |
| Linux host with two idle GPUs | `host-gpu-load` (`host/gpuLoadUtilization`, manual: name it) | Also Docker, GPU runtime, DCGM image, and `nv-hostengine` and `dcgmi` on `PATH` | None |
| Linux host with two GPUs, one idle | `host-gpu-bind-unbind` (`host/gpuBindUnbindEvents`, manual: name it) | An idle MIG-off GPU bound to the `nvidia` driver with no process holding its device node open (needs `fuser` from psmisc), another MIG-off GPU that stays bound, passwordless sudo, driver 590 or later, persistence mode off and `nvidia-persistenced` not running, no NVLink fabric and `nvidia-fabricmanager` not running | None |
| Linux host with two or more GPUs, all MIG-capable | `host-mig-two-instances-per-gpu` (`host/migGpuLevelFields`, manual: name it) | Passwordless sudo, no GPU compute process and no MIG instance before the run | None |
| Container runtime with GPU | `container-*` | Docker, GPU runtime | `test-e2e-container` (`task test:e2e:container`; manual, optional) |
| Kubernetes cluster | `k8s` groups | Kubeconfig or local k3d, plus GPU, MIG, DRA, IPv6 or GPU Operator where the group needs it | None |

Fixture flags:

- `--dcgm-nvml-injection-yaml` is required by `host/nvmlInjectionMetrics`,
  `host/migXidCounters` and `host/configServesMetrics`. It sets
  `NVML_INJECTION_MODE` and `NVML_YAML_FILE`.
- `--dcgm-nvsdm-mock-yaml` is for `host/nvsdmMockMetrics`.
- The two flags are mutually exclusive.

### Host config scenarios

Scenarios that never start DCGM need no GPU or fixture. The GPU-disabled
`test-e2e-dcgm-mocks` job (`task test:e2e:mocks`) runs them with only the
exporter binary:

| Scenario | Checks |
| --- | --- |
| `host/configStartupErrors` | A bad `--config-file`, flag or environment value makes the exporter exit with status 1 within a timeout, print nothing to stdout, and print a stderr message that names the YAML path, flag or variable. When a config file is involved, the message also names the file, unless it already gives the YAML path's location. Helpers: `runExporterCommand`, `runExporterExpectingStartFailure` in `tests/host/helpers_test.go`. |
| `host/configInspection` | `config show`, `config validate` and `config explain`: exit statuses 0, 1 and 2, empty stdout on failure, JSON output, the not-checked list, `file:line:column` with the YAML path, the version 1 warning, the `show` round trip, flag over environment over file for the listen address, log format and devices, and no Go field names in output. |

Config files for these are in `tests/host/testdata/config/`.

The `host-config-injection` group serves `/metrics` from a YAML config file:
`host/configServesMetrics`, `configFile`, `configPrecedence`, `watchGroups`,
`configEnrichment`, `hpcJobMapping` and `reload`.

- Only `host/configServesMetrics` requires the fixture. The others use it when
  `--dcgm-nvml-injection-yaml` is given and run on the host's own GPUs when not.
- `test-e2e-host` runs the group on the GPU (without `configServesMetrics`).
  `test-e2e-dcgm-mocks` runs all seven in one invocation with `H200.yaml`.
- Scenarios that use the injection file cannot share an invocation with
  scenarios that do not.

Cache-count checks in `host/configFile`, `host/watchGroups` and
`host/configPrecedence` start a private `nv-hostengine` on a loopback port and
point the exporter at it with `--remote-hostengine-info`.

- `nv-hostengine` must be on `PATH` and run as the test user (no root). The
  checks fail without it.
- The hostengine inherits the NVML injection variables, so no GPU is needed.
- With a fixed `max_keep_age`, a field's cached sample count is the age divided
  by its update interval. A file `every`, an override `every`, an override
  sample limit, and flag, environment and file precedence each give a different
  count, so ignoring a setting fails the check.
- `host/injectedEventCounts` and `host/gpuLoadUtilization` start the same kind
  of private hostengine on a Unix socket, against the host's real GPUs rather
  than NVML injection, and also need `dcgmi` on `PATH`; they fail without
  either. Both tools are installed with `datacenter-gpu-manager-4-core`, the
  DCGM host package the `host-dcgm` bootstrap component installs; a host that
  only provides `libdcgm.so.4` passes the host suite check but fails these
  scenarios.

`static/chartMounts` renders the chart with a set of value combinations
and fails on any `subPath` mount, in the renders or in the template source. It
also checks the chart's mount paths and that the chart refuses metrics-list
values the exporter would not read. `k8s/defaultChartMounts` runs the default chart on a
cluster and checks that an edited `customMetrics` reaches the running pod. See
the [Kubernetes tests guide](../../../tests/k8s/README.md#check-the-charts-mounts).

Static chart checks cannot run the cgo product binary. `static/chartConfigRead`
reads the rendered ConfigMaps and the README `config` examples with the
exporter's YAML reader and applies them. It does not run the checks against the
metric file; the host suite runs `config validate` on real files.

### Injection and mock evidence

NVML injection and NVSDM mocks compare exporter metrics with direct DCGM
queries. Record the tested fields, device and link types, why fields are
unavailable, and the DCGM version. Live hardware verifies topology, allocation,
device changes and timing.

### GPU inventory from nvidia-smi

`host/startupMetrics`, `host/defaultPreset`, `container/imageStartup` and
`k8s/default` compare exporter output with `nvidia-smi --query-gpu`, joined by
GPU UUID (`tests/internal/nvidiasmi`, `tests/internal/metriccontract`).

- Identity, on every sample that has a UUID label: `modelName` equals `name`,
  `pci_bus_id` equals `pci.bus_id` (ignoring case), `gpu` equals `index`,
  `device` is `nvidia` plus `gpu`, one identity per UUID, and the hostname
  label is the host name (the Docker host's name in the container suite, the
  node name on Kubernetes). The reported UUIDs
  must equal the listed ones.
- Values, for the families and label rows in the run's CSV: framebuffer
  capacity within 1%, free + used + reserved within 1% of capacity,
  temperature within 5 °C, memory clock within 1%, SM clock within 10%, power
  within the larger of 20 W or 15%, and the driver version label. A family
  that the CSV does not enable or the scrape lacks is not checked and is not
  reported. A value `nvidia-smi` does not report (any bracketed text such as
  `[N/A]`) is not compared, and the reason is logged. A GPU whose enabled
  checks were all skipped or found no sample is logged as having no value
  compared; that is not a failure. A GPU without a sample of a checked family
  fails unless it is in MIG mode, a GPU without the driver version label fails
  when the CSV has that row, and a sample of a checked family with no UUID
  label fails.
- Each comparison scrapes and queries `nvidia-smi` again until they agree or
  the timeout passes, because the two sample at different times.
- Where `nvidia-smi` runs: on the test host for the host and container suites
  and for the local k3d cluster the CLI created (the CLI passes
  `-owned-local-cluster` to the Kubernetes test binary). On any other cluster
  it runs in the workload pod; only that pod's GPU is compared, without the
  `gpu` index, which `nvidia-smi` numbers inside the pod.
- A missing `nvidia-smi` fails the scenario as a test failure
  (exit status 20) with a message that starts with `setup:`; any other
  `nvidia-smi` failure retries until the timeout in all three suites. The
  Kubernetes suite treats a pod exec error that names a missing executable
  as a missing `nvidia-smi`. The host suite
  skips the comparison under NVML injection, and the container suite skips it
  when the Docker endpoint (`DOCKER_HOST`, or the current Docker context's
  endpoint when unset) is not a Unix socket.

### Output equals the run's CSV

`host/startupMetrics`, `host/defaultPreset`, `host/ipv6Listen`,
`host/systemdSocket`, `host/gpuBindUnbindWatch`, `container/imageStartup` and
`k8s/default` check that the output is exactly the CSV the exporter runs with
(`tests/internal/metriccontract`):

- Every emitted `DCGM_FI_*` family is a metric row of the CSV, with the row's
  type and the row's help text as its HELP line. A label row emitted as a
  family fails. Exporter-owned `DCGM_EXP_*` families are not checked against
  the CSV.
- A row the exporter does not emit is classified by `E2E_DCGM_FIELD_SUPPORT`,
  one JSON document keyed by CSV names:
  `{"supported": [...], "unsupported": {"<name>": "<reason>"}}`. A supported
  row fails, naming the row. An unsupported row is reported as a skip with
  the probe's reason. A row the document does not list, or any row when the
  variable is unset, is reported as an unqualified skip, never a pass.
- The scenarios wait, within their startup or retry deadline, until every
  supported row is emitted, because some rows have no value in their first
  samples.
- Where the CLI runs `dcgmi dmon -e <ids> -c 3`, and prints that DCGM's
  version in its "DCGM field support" section:
  - Host suite: the host's `nv-hostengine`. When none is reachable, the DCGM
    image, but its result is used only when the image has the host's DCGM
    version; otherwise nothing is produced and the reason is printed.
  - Container suite: the DCGM image, on the Docker host that runs the
    exporter image.
  - Local k3d cluster the CLI created: the DCGM image the capability probes
    use (`--dcgm-image`, the run's DCGM version), or the host DCGM they fall
    back to when that image is not published for the host platform.
  - For the container suite and the local cluster, the result is used only
    when the exporter image the run uses (`--exporter-image`) carries the
    probe's DCGM version in its `com.nvidia.dcgm.version` label. When it
    differs, or the image is not local or has no label, nothing is produced
    and the reason is printed.
  - A GPU Operator group whose exporter reads another DCGM gets the result
    only when that DCGM has the probe's version: the GPU Operator exporter
    image's label when it is not the probed exporter image, and the
    `--gpu-operator-dcgm-image` version for `gpuOperatorMainRemote` and
    `gpuOperatorMainDRA`. Otherwise that group gets none and the reason is
    printed.
- How rows are classified:
  - Each row of `etc/default-counters.csv` is mapped to its DCGM field ID with
    go-dcgm's name table, the lookup the exporter uses.
  - A row with a value on any GPU in any sample is supported.
  - N/A or a DCGM blank value on every GPU in every sample is listed as
    unsupported with the reason "DCGM returned no value (N/A) on every GPU
    during the probe". That is not proof DCGM lacks the field: event fields
    such as XID errors (230) and retired pages (390) stay N/A until an event
    happens. So such a row is only not required: a missing one is a skip,
    never a failure.
  - Profiling fields are N/A in the first sample and have values afterwards,
    which is why each field gets 3 samples. They are probed in their own
    batch. When DCGM refuses a batch, it is probed again in halves.
  - Rows that are not DCGM fields, or whose probe failed, are left out of the
    document.
- The host suite gets the document for scenarios of `host-baseline` and
  `host-default-preset`, which run on the host's own GPUs, except
  `configStartupErrors` and `configInspection`, which never start DCGM.
- On a cluster the CLI did not create, nothing is produced.
- Kubernetes scenarios other than `k8s/default` use the document for missing
  rows but do not check for extra families or HELP text, because they may run
  with another CSV.
- A value set in the environment before the run is used as given.

## Run behavior

- Stages (static, host, container, Kubernetes) are prepared when reached.
  Earlier results are kept, independent work continues after a failure,
  dependent work is blocked, and cancellation stops scheduling.
- The CLI installs selected dependencies and applies hardware settings that need
  no reboot, and reports each change. It never reboots. A required reboot blocks
  the affected tests.
- Cleanup removes owned resources, including leftovers from earlier runs with
  the same name, and restores temporary host and runtime settings even when
  resources are kept (`--k3d-keep-cluster`). Cleanup errors are reported.
- MIG changes on the host:
  - Need Linux; other platforms fail before the profile probe.
  - Hold an exclusive GPU 0 lock (`/run/dcgm-exporter-e2e/mig-gpu-0.lock`,
    root-owned, kept between runs) from the first state check until restoration.
  - Make a second E2E run that asks for MIG configuration fail immediately.
    The lock only coordinates E2E runs; other MIG writers must be kept away
    separately.
- A scenario can declare the MIG layout it runs on (`MIGLayout` in the
  catalog). `host/migGpuLevelFields` declares `two-instances-per-gpu`: every
  GPU in MIG mode with two GPU instances, each with a compute instance, of the
  largest profile that fits twice. Before the host suite runs, the CLI applies
  the declared layout under the same lock and safety checks, and passes
  `E2E_MIG_LAYOUT` to the tests; when the suite ends it restores MIG and the
  CDI spec. A host with fewer than two GPUs or a GPU without MIG skips the
  suite. A layout cannot share a run with scenarios that declare none.
- `--mig-configure` defaults to `auto`, which uses MIG as it is; `true` or a
  profile ID changes MIG. MIG scenarios plan from the current MIG state: they
  need a GPU with MIG on and a DCGM GPU instance, and mixed MIG also needs a GPU
  with MIG off.
- Optional hardware scenarios are skipped when a capability probe proves the
  feature unsupported. MIG scenarios are skipped when no GPU has MIG on with a
  DCGM GPU instance and the run may not change MIG. Mixed MIG scenarios are
  also skipped when no GPU has MIG off, because a MIG change does not turn MIG
  off. A requested MIG change that leaves no usable MIG state fails planning,
  unless the GPU advertises no MIG profiles; then MIG scenarios are skipped.
- In CLI runs, these scenarios also skip, instead of failing, when an input is
  missing:

  | Scenario | Skips when |
  | --- | --- |
  | `host` suite | `libdcgm.so.4` is not found and the suite was not named with `--suite` or `--scenario`. Named, it fails. |
  | `host/ipv6Listen` | The IPv6 loopback address cannot be used. |
  | `host/gpuLoadUtilization` | `nvidia-smi` shows fewer than two GPUs with MIG off, no compute process and `utilization.gpu` below 20%, or a CUDA version older than 12. |
  | `k8s/nvswitch`, `k8s/nvswitchThroughput`, `k8s/graceCpu` and `k8s/migSpecificInstanceSelection` (selection steps) | The default run's scrape shows no more switches, links, CPUs, cores or MIG instances than the step selects. |
  | `host/gpuLinkFields` | No NVLink is up. `E2E_DCGM_HOSTENGINE_ADDRESS` must name a running private `nv-hostengine`; the CLI fails without it. |
  | `container/remoteDcgmUri` (VSOCK specs) | `/dev/vsock` is absent, for example with `--bootstrap-component vsock=skip`. |
  | `static/chartDefaultMetrics` (source chart defaults) | The chart's metrics file is not a symlink, as in the packaged chart. |
  | `static` chart scenarios that load the source chart | Git checked the metrics symlink out as a plain file (no symlink support). |
  | `static/watchGroupsSelection`, `static/hostSuiteImports` | There is no source checkout or Go toolchain. |

  The CLI makes the rest fail instead of skipping: host remote DCGM, host
  VSOCK and host profiling scenarios fail when their prerequisite is missing,
  and every image a selected scenario needs, including `--dcgm-image`, must be
  present locally or published in a registry for the host platform.
- Required CI checks run or fail. Go and Ginkgo results are preserved, and
  blocked work or unavailable runners are reported as not run.
