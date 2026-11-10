# Validation Recipes

Run commands from the repository root.

## Common Docs And Go Gates

```bash
go test ./internal/pkg/counters
task test:unit
task lint
task fmt:check
task validate
```

## Conditional Gates

Generated mocks or go:generate inputs:

```bash
task generate
git diff --exit-code
```

Version-derived files:

```bash
task deps:sync
task deps:sync:check
```

Docker image behavior:

```bash
task build:image:local
task build:image:verify
task test:container
```

GPU/DCGM integration behavior:

```bash
task test:host
task build:e2e
task test:e2e:host
```

E2E bundle checks without a GPU (`test:e2e:mocks` needs root, a DCGM
development build with NVML injection, `nv-hostengine`, `dcgmi`, and fixtures
matching `DCGM_VERSION`):

```bash
task build:e2e
task test:e2e:smoke
task test:e2e:mocks NVML_INJECTION_DIR=/path NVSDM_MOCK_DIR=/path
```

GPU Kubernetes behavior:

```bash
task test:k8s
task test:e2e
```

If GPU, Docker, Kubernetes, DCGM, or network prerequisites are unavailable,
record the skip and reason in the MR.
