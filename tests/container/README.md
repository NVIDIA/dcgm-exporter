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
make local
make test-integration-container
```

The `tests/container/Makefile` is a focused local-suite helper for Ginkgo-specific image variants. Use it when iterating inside this directory, but keep the root Makefile target as the public entrypoint.

Container live scenarios use Ginkgo labels:

- `imageStartup`: image pull/startup, lifecycle, health, and metrics behavior.
- `configuration`: runtime configuration and collector behavior.
- `invalidDeviceSelectors`: invalid `DCGM_EXPORTER_DEVICES_STR` diagnostics.
- `remoteDcgmUri`: remote DCGM URI parsing, including TCP, Unix socket, and VSOCK forms. This uses `DCGM_IMAGE` for `nv-hostengine` and the configured `EXPORTER_*_IMAGE` values for DCGM Exporter.

For focused iteration, run the package with a label filter:

```bash
go test --tags=container ./tests/container -v \
  -args --ginkgo.label-filter=remoteDcgmUri
```

Pass `--result-markers` to emit `&&&&` markers for each executed spec.
`--no-result-markers` disables both the container suite lifecycle and per-spec
markers. Runtime Ginkgo skips are reported as `WAIVED`.

## Usage

### Default Behavior

By default, tests run against locally built images with the current version:
<!-- sync:image-tags:start -->
- `nvidia/dcgm-exporter:4.6.0-4.8.3-distroless`
- `nvidia/dcgm-exporter:4.6.0-4.8.3-ubuntu26.04`
<!-- sync:image-tags:end -->

(Version is read from `hack/versions.env`; run `make sync-versions` after bumping)

```bash
cd tests/container
make container-test
```

### Test Specific Variant Only

Use dedicated targets to test individual variants:

```bash
# Test only Ubuntu. Build this support image locally or provide its location.
make container-test-ubuntu

# Test only distroless
make container-test-distroless
```

### Custom Images

#### Change Registry or Version

```bash
# Test from your own registry
REGISTRY=my-registry.io FULL_VERSION=3.0.0-3.1.0 make container-test

# Test specific version
FULL_VERSION=4.5.3-5.0.0 make container-test-ubuntu
```

#### Override Specific Images

Set environment variables to test specific images:

```bash
# Test a custom Ubuntu support image
EXPORTER_UBUNTU_IMAGE=registry.example/dcgm-exporter:4.6.0-4.8.2-ubuntu26.04 \
make container-test-ubuntu

# Mix exporter images from different locations
EXPORTER_UBUNTU_IMAGE=registry.example/dcgm-exporter:4.6.0-4.8.2-ubuntu26.04 \
EXPORTER_DISTROLESS_IMAGE=my-registry.io/dcgm-exporter:custom-distroless \
make container-test

# Test remote DCGM URI behavior against a matching DCGM image
DCGM_IMAGE=nvcr.io/nvidia/cloud-native/dcgm:4.6.0-1-ubuntu24.04 \
EXPORTER_DISTROLESS_IMAGE=my-registry.io/dcgm-exporter:custom-distroless \
go test --tags=container ./tests/container -v \
  -args --ginkgo.label-filter=remoteDcgmUri

# Test exporter images from different registries
EXPORTER_UBUNTU_IMAGE=registry1.io/dcgm-exporter:ubuntu \
EXPORTER_DISTROLESS_IMAGE=registry3.io/dcgm:distroless \
make container-test
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REGISTRY` | `nvidia` | Container registry for default images |
<!-- sync:full-version-example:start -->
| `FULL_VERSION` | `4.6.0-4.8.3` | Combined DCGM and exporter version (read from hack/versions.env) |
<!-- sync:full-version-example:end -->
| `DCGM_IMAGE` | `EXPORTER_UBUNTU_IMAGE` | DCGM-capable image used to run `nv-hostengine` for remote URI tests |
| `EXPORTER_UBUNTU_IMAGE` | `${REGISTRY}/dcgm-exporter:${FULL_VERSION}-ubuntu26.04` | Full path to Ubuntu debug/support image |
| `EXPORTER_DISTROLESS_IMAGE` | `${REGISTRY}/dcgm-exporter:${FULL_VERSION}-distroless` | Full path to distroless image |

**Note:** Set any `EXPORTER_*_IMAGE` variable to empty string (`""`) to skip testing that variant.

## Common Scenarios

```bash
# Test only one variant from a custom registry
EXPORTER_UBUNTU_IMAGE=registry.example/dcgm-exporter:4.6.0-4.8.2-ubuntu26.04 \
EXPORTER_DISTROLESS_IMAGE="" \
make container-test

# Test release candidate
FULL_VERSION=4.5.3-5.0.0-rc1 make container-test

# Test PR build
REGISTRY=ci.mycompany.com \
FULL_VERSION=4.5.3-pr-1234 \
make container-test-ubuntu

# Compare two versions
EXPORTER_UBUNTU_IMAGE=nvidia/dcgm-exporter:4.6.0-4.8.2-ubuntu26.04 \
EXPORTER_DISTROLESS_IMAGE=nvidia/dcgm-exporter:4.6.0-5.0.0-distroless \
make container-test
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
- DCGM metrics are returned (requires GPU)

#### Health Endpoint
- `/health` returns HTTP 200

### Test Structure

```
tests/container/
├── suite_test.go           # Test suite setup
├── image_startup_test.go   # Startup and lifecycle tests
├── remote_dcgm_uri_test.go  # Remote DCGM URI and vsock tests
├── env_test.go             # Test configuration helper coverage
├── helpers_test.go         # Docker helper functions
├── Makefile                # Focused local-suite helper targets
└── README.md              # This file
```

## Version Updates

When updating DCGM or exporter versions, edit `hack/versions.env` at the
project root (change `DCGM_VERSION` and/or `EXPORTER_VERSION`), then
propagate to all derived files:

```bash
# From the project root
make sync-versions
```

The test Makefiles read `FULL_VERSION` directly from `hack/versions.env`
via `include`, so they pick up changes automatically. Static files (READMEs,
YAML manifests) are updated by `sync-versions`. See `CONTRIBUTING.md` for
the full workflow.

After version update:
1. Build new images: `make local`
2. Run tests: `make test-integration-container`

## Requirements

- Docker daemon running
- Go installed at the version pinned by this repository
- Images must be built locally or available in registry
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
docker run --rm --gpus all nvidia/cuda:13.1.1-base nvidia-smi
```

Verify DCGM access:

<!-- sync:docker-run-example:start -->
```bash
docker run --rm --gpus all --cap-add SYS_ADMIN nvcr.io/nvidia/k8s/dcgm-exporter:4.6.0-4.8.3-distroless
```
<!-- sync:docker-run-example:end -->

## Limitations

- Tests require Docker to be available
- Tests require NVIDIA GPU hardware and Container Toolkit
- Without GPU, containers will fail to start or return empty metrics

## Port Management

Tests automatically find and use available ports for each container, eliminating port conflicts. Runtime checks use host networking and configure dcgm-exporter to listen on a unique random host port so they avoid Docker bridge/proxy behavior while still keeping each test isolated.
