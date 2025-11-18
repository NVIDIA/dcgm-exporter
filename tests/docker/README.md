# Docker Image Tests

Go-based tests for validating DCGM Exporter Docker images using Ginkgo/Gomega.

## Overview

These tests validate that Docker images:
- Exist locally or can be pulled
- Start successfully
- Serve metrics on `/metrics` endpoint (Prometheus format)
- Serve health checks on `/health` endpoint

## Quick Start

```bash
# Build images locally
make local

# Test all images
make test-images
```

## Usage

### Default Behavior

By default, tests run against locally built images with the current version:
- `nvidia/dcgm-exporter:4.4.2-4.7.0-ubuntu22.04`
- `nvidia/dcgm-exporter:4.4.2-4.7.0-ubi9`
- `nvidia/dcgm-exporter:4.4.2-4.7.0-distroless`

(Version is automatically updated by `make update-version` from the root Makefile)

```bash
cd tests/docker
make docker-test
```

### Test Specific Variant Only

Use dedicated targets to test individual variants:

```bash
# Test only Ubuntu
make docker-test-ubuntu

# Test only UBI
make docker-test-ubi

# Test only distroless
make docker-test-distroless
```

### Custom Images

#### Change Registry or Version

```bash
# Test from your own registry
REGISTRY=my-registry.io FULL_VERSION=3.0.0-3.1.0 make docker-test

# Test specific version
FULL_VERSION=4.5.0-5.0.0 make docker-test-ubuntu
```

#### Override Specific Images

Set environment variables to test specific images:

```bash
# Test published image
IMAGE_UBUNTU=nvcr.io/nvidia/k8s/dcgm-exporter:4.4.2-4.7.0-ubuntu22.04 \
make docker-test-ubuntu

# Mix local and published images
IMAGE_UBUNTU=nvcr.io/nvidia/k8s/dcgm-exporter:4.4.2-4.7.0-ubuntu22.04 \
IMAGE_UBI="" \
IMAGE_DISTROLESS=my-registry.io/dcgm-exporter:custom-distroless \
make docker-test

# Test from different registries
IMAGE_UBUNTU=registry1.io/dcgm:ubuntu \
IMAGE_UBI=registry2.io/dcgm:ubi \
IMAGE_DISTROLESS=registry3.io/dcgm:distroless \
make docker-test
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REGISTRY` | `nvidia` | Container registry for default images |
| `FULL_VERSION` | `4.4.2-4.7.0` | Combined DCGM and exporter version (updated by root Makefile) |
| `IMAGE_UBUNTU` | `${REGISTRY}/dcgm-exporter:${FULL_VERSION}-ubuntu22.04` | Full path to Ubuntu image |
| `IMAGE_UBI` | `${REGISTRY}/dcgm-exporter:${FULL_VERSION}-ubi9` | Full path to UBI image |
| `IMAGE_DISTROLESS` | `${REGISTRY}/dcgm-exporter:${FULL_VERSION}-distroless` | Full path to distroless image |

**Note:** Set any `IMAGE_*` variable to empty string (`""`) to skip testing that variant.

## Common Scenarios

```bash
# Test only one variant from published registry
IMAGE_UBUNTU=nvcr.io/nvidia/k8s/dcgm-exporter:4.4.2-4.7.0-ubuntu22.04 \
IMAGE_UBI="" \
IMAGE_DISTROLESS="" \
make docker-test

# Test release candidate
FULL_VERSION=4.5.0-5.0.0-rc1 make docker-test

# Test PR build
REGISTRY=ci.mycompany.com \
FULL_VERSION=4.4.2-pr-1234 \
make docker-test-ubuntu

# Compare two versions
IMAGE_UBUNTU=nvidia/dcgm-exporter:4.4.2-4.7.0-ubuntu22.04 \
IMAGE_DISTROLESS=nvidia/dcgm-exporter:4.5.0-5.0.0-distroless \
IMAGE_UBI="" \
make docker-test
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
tests/docker/
├── docker_suite_test.go    # Test suite setup
├── image_startup_test.go   # Startup and lifecycle tests
├── image_metrics_test.go   # Metrics and health tests
├── helpers.go              # Docker helper functions
├── Makefile               # Test targets
└── README.md              # This file
```

## Version Updates

When updating DCGM or exporter versions, the tests are automatically updated:

```bash
# From the project root
make update-version \
  OLD_DCGM_VERSION=4.3.0 \
  NEW_DCGM_VERSION=4.4.2 \
  OLD_EXPORTER_VERSION=4.5.0 \
  NEW_EXPORTER_VERSION=4.7.0

# This will update FULL_VERSION in tests/docker/Makefile from 4.3.0-4.5.0 to 4.4.2-4.7.0
```

After version update:
1. Build new images: `make local`
2. Run tests: `make test-images`

## Requirements

- Docker daemon running
- Go 1.21+ (for running tests)
- Images must be built locally or available in registry
- **NVIDIA GPU** with drivers installed
- **NVIDIA Container Toolkit** (`nvidia-docker2`) for GPU access
- **Docker configured** with NVIDIA runtime

### GPU Setup

Containers are started with the following flags for GPU access:
- `--gpus all` - Grants access to all available GPUs
- `--cap-add SYS_ADMIN` - Required capability for DCGM hardware access

To verify your GPU setup:
```bash
# Check NVIDIA Container Toolkit
docker run --rm --gpus all nvidia/cuda:12.0-base nvidia-smi

# Verify DCGM access
docker run --rm --gpus all --cap-add SYS_ADMIN nvcr.io/nvidia/cuda:13.0.1-base-ubuntu22.04 nvidia-smi
```

## Limitations

- Tests require Docker to be available
- Tests require NVIDIA GPU hardware and Container Toolkit
- Without GPU, containers will fail to start or return empty metrics

## Port Management

Tests automatically find and use available ports for each container, eliminating port conflicts. Each test container binds to a unique random port on the host, mapped to the container's port 9400. This allows multiple tests to run safely without port collisions.
