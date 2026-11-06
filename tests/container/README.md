# Container Runtime Tests

Go-based tests for validating DCGM Exporter container images using Ginkgo/Gomega.

## Overview

These tests validate that Docker images:
- Exist locally or can be pulled
- Start successfully
- Serve metrics on `/metrics` endpoint (Prometheus format)
- Serve health checks on `/health` endpoint

## Quick Start

Run from the repository root:

```bash
task build:image:local
task test:container
```

Use the root Taskfile.yml to run the container suite.

Container live scenarios use Ginkgo labels:

- `imageStartup`: image pull/startup, lifecycle, health, and metrics behavior.
- `dcgmLibraryFailure`: clean startup failure when `libdcgm.so.4` is unavailable through both the loader cache and `LD_LIBRARY_PATH`. It empties the image's `libdcgm.so.4` inside a throwaway container and needs no GPU.
- `configuration`: runtime configuration and collector behavior.
- `invalidDeviceSelectors`: invalid `DCGM_EXPORTER_DEVICES_STR` diagnostics.
- `remoteDcgmUri`: remote DCGM URI parsing, including TCP, Unix socket, and VSOCK forms. This uses `DCGM_IMAGE` for `nv-hostengine` and `EXPORTER_DISTROLESS_IMAGE` for DCGM Exporter.

For focused iteration, run the package with a label filter:

```bash
# Requires --tags=container; without it the package has no tests.
go test --tags=container ./tests/container -v \
  -args --ginkgo.label-filter=remoteDcgmUri
```

For native runs, set `E2E_SUITE_RESULT_MARKERS=true` to emit `&&&&` markers for
each executed spec. For E2E CLI runs, pass `--result-markers`;
`--no-result-markers` disables both the container suite lifecycle and per-spec
markers. Runtime Ginkgo skips are reported as `WAIVED`.

The `imageStartup` spec ID includes the scenario label and image variant:

```text
dcgm_exporter_e2e_container_image_startup_should_validate_distroless_image
```

## Usage

### Default Behavior

By default, tests run against the locally built image with the current version:
<!-- sync:image-tags:start -->
- `docker.io/nvidia/dcgm-exporter:4.9.0`
<!-- sync:image-tags:end -->

(Version is read from `hack/versions.env`; run `task deps:sync` after bumping)

Run `task test:container` from the repository root.

`task test:container:distroless` is the same as `task test:container`.

### Custom Images

#### Change Registry or Version

```bash
# Test from your own registry
REGISTRY=my-registry.io VERSION=X.Y.Z task test:container

# Test a specific version; replace X.Y.Z with an image tag such as 4.8.2
VERSION=X.Y.Z task test:container
```

#### Override Specific Images

Set environment variables to test specific images:

```bash
# Test a custom distroless image
EXPORTER_DISTROLESS_IMAGE=my-registry.io/dcgm-exporter:custom-distroless \
task test:container

# Test remote DCGM URI behavior against a matching DCGM image
# sync:dcgm-gpu-check-example:start
DCGM_IMAGE=nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04@sha256:42bdc58249e05e963ac16d4c1437de4e00a9b258ae55bfc478a2e0de216f42cd \
EXPORTER_DISTROLESS_IMAGE=my-registry.io/dcgm-exporter:custom-distroless \
go test --tags=container ./tests/container -v \
  -args --ginkgo.label-filter=remoteDcgmUri
# sync:dcgm-gpu-check-example:end
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `E2E_DCGM_FIELD_SUPPORT` | Empty | DCGM field support evidence for the tested GPUs, as a JSON document (see below). The e2e CLI sets it; set it yourself for direct `go test` runs |
| `REGISTRY` | `nvidia` | Container registry for default images |
<!-- sync:version-example:start -->
| `VERSION` | `4.9.0` | Exporter version, used as the image tag (read from hack/versions.env) |
<!-- sync:version-example:end -->
| `DCGM_IMAGE` | Pinned DCGM image from `hack/versions.env` with `task test:container`; empty with direct `go test` | DCGM-capable image used to run `nv-hostengine` for remote URI tests. With direct `go test`, the `remoteDcgmUri` specs skip when it is empty or the image is not local. `task test:container` pulls the pinned image when it is missing and warns if it cannot |
| `EXPORTER_DISTROLESS_IMAGE` | `${REGISTRY}/dcgm-exporter:${VERSION}` | Full path to distroless image |

**Note:** Set `EXPORTER_DISTROLESS_IMAGE` to an empty string (`""`) to skip the exporter image specs.

## Common Scenarios

```bash
# Test release candidate
VERSION=X.Y.Z.rc1 task test:container

# Test PR build
REGISTRY=ci.mycompany.com \
VERSION=pr-1234 \
task test:container

# Test another version of the image
EXPORTER_DISTROLESS_IMAGE=nvidia/dcgm-exporter:VERSION_B \
task test:container
```

## Test Details

### What Gets Tested

#### Image Existence
- Verifies image exists locally or can be pulled

#### Container Startup
- Container starts successfully
- Container produces logs
- No panic errors in logs

#### Container Lifecycle
- Containers stop gracefully
- No hanging processes

#### Metrics Endpoint
- `/metrics` returns HTTP 200
- Response is valid Prometheus text format
- The existing baseline GPU families have valid samples before the 120-second readiness deadline
- The output is exactly the shipped default CSV: every emitted metric row has the
  CSV's type, the CSV's help text as its HELP line, and GPU identity labels on
  every sample, and every emitted `DCGM_FI_*` family is a CSV row
- Label-only rows are checked for a non-empty field-named label on at least one `DCGM_FI_` sample
- A CSV row the exporter does not emit fails when `E2E_DCGM_FIELD_SUPPORT` lists
  it as supported, and is reported as a skip, with the reason, when it lists it
  as unsupported or does not list it
- GPU identity labels and values match `nvidia-smi` on the test host, joined by GPU UUID; skipped when the Docker endpoint (`DOCKER_HOST`, or the current Docker context's endpoint when unset) is not a Unix socket

`E2E_DCGM_FIELD_SUPPORT` holds one JSON document keyed by CSV names:

```json
{"supported": ["DCGM_FI_DEV_GPU_UTIL_RATIO"], "unsupported": {"DCGM_FI_DEV_MEMORY_TEMP_CELSIUS": "N/A on every GPU"}}
```

The e2e CLI produces it with `dcgmi dmon` in the DCGM image on the Docker host.
For a direct run, build it from a DCGM query on the tested GPUs with the
matching DCGM version; the exporter's `/metrics` output is not support evidence.
Every name must be an enabled row of the shipped default CSV; invalid input
fails before readiness polling. Without the variable, missing rows are reported
as unqualified coverage, never as a pass. For example:

```bash
E2E_DCGM_FIELD_SUPPORT='{"supported":["DCGM_FI_DEV_MEMORY_TEMP_CELSIUS"]}' \
go test --tags=container ./tests/container -v \
  -args --ginkgo.label-filter=imageStartup
```

#### Health Endpoint
- `/health` returns HTTP 200

### Test Structure

```
tests/container/
├── suite_test.go                # Test suite setup
├── image_startup_test.go        # Startup and lifecycle tests
├── dcgm_library_loading_test.go # Clean failure when libdcgm.so.4 is unusable
├── remote_dcgm_uri_test.go      # Remote DCGM URI and vsock tests
├── env_test.go                  # Test configuration helper coverage
├── helpers_test.go              # Docker helper functions
└── README.md              # This file
```

## Version Updates

When updating DCGM or exporter versions, edit `hack/versions.env` at the
project root (change `DCGM_VERSION` and/or `EXPORTER_VERSION`), then
propagate to all derived files:

```bash
# From the project root
task deps:sync
```

The root Task commands read `EXPORTER_VERSION` directly from `hack/versions.env`,
so they pick up changes automatically. Static files (READMEs, YAML manifests)
are updated by `task deps:sync`. See `CONTRIBUTING.md` for the full workflow.

After version update:
1. Build new images: `task build:image:local`
2. Run tests: `task test:container`

## Requirements

- Docker daemon running
- Go installed at the version pinned by this repository
- The image must be built locally or available in registry
- **NVIDIA GPU** with drivers installed
- **NVIDIA Container Toolkit** (`nvidia-docker2`) for GPU access
- **Docker configured** with NVIDIA runtime

### GPU Setup

Containers are started with the following flags for GPU access:
- `--gpus all` - Grants access to all available GPUs
- `--privileged` - Grants DCGM the device and capability access needed by MIG-capable systems

To verify your GPU setup:
```bash
# Check NVIDIA Container Toolkit
# sync:cuda-gpu-check-example:start
docker run --rm --gpus all nvcr.io/nvidia/cuda:13.4.3-base-ubuntu26.04@sha256:e2eb43e25d404e2c48716f80afdb8989a84c4da588f5509330322842084d97b0 nvidia-smi
# sync:cuda-gpu-check-example:end
```

Verify DCGM access:

<!-- sync:docker-run-example:start -->
```bash
docker run --rm --gpus all --cap-add SYS_ADMIN nvcr.io/nvidia/k8s/dcgm-exporter:4.9.0
```
<!-- sync:docker-run-example:end -->

## Limitations

- Tests require Docker to be available
- Tests require NVIDIA GPU hardware and Container Toolkit
- Without GPU, containers will fail to start or return empty metrics

## Port Management

Tests automatically find and use available ports for each container, eliminating port conflicts. Runtime checks use host networking and configure dcgm-exporter to listen on a unique random host port so they avoid Docker bridge/proxy behavior while still keeping each test isolated.
