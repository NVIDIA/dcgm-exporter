# Validation Recipes

Run commands from the repository root.

## Common Docs And Go Gates

```bash
go test ./internal/pkg/counters
make test-main
make lint
make check-fmt
make validate
```

## Conditional Gates

Generated mocks or go:generate inputs:

```bash
make generate
git diff --exit-code
```

Version-derived files:

```bash
make sync-versions
make validate-versions
```

Docker image behavior:

```bash
make local
make test-images
```

GPU/DCGM integration behavior:

```bash
make test-integration
```

GPU Kubernetes behavior:

```bash
make e2e-local-check
make e2e-local-up
make e2e-local-deploy
make test-e2e
```

If GPU, Docker, Kubernetes, DCGM, or network prerequisites are unavailable,
record the skip and reason in the MR.
