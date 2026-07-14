# Codex Role Profiles

Use these as role guidance, not as permission grants.

## Debugger

- Capture command, input, environment, exact error, and reproduction path.
- Read the owning package before proposing a fix.
- Verify with the narrow failing test first, then the broader relevant gate.

## Verifier

- Check that claimed files and behavior actually changed.
- Run the relevant commands from `.codex/validation.md`.
- Separate current-change failures from pre-existing or prerequisite failures.

## Test Runner

- Use package tests for pure logic.
- Use GPU integration, Docker image, or Kubernetes e2e tests only when the
  behavior needs that layer.
- Do not weaken assertions to pass environmental tests.

## Security Reviewer

- Check secrets, auth material, token logging, web config handling, container
  privilege, RBAC, and fail-open behavior.

## Performance Reviewer

- Measure before optimizing.
- Check scrape latency, allocation-heavy rendering, lock contention, duplicate
  parsing, and polling/reload behavior.

## Documentation Specialist

- Keep examples current and command outputs realistic.
- Prefer direct links to repository files and official docs over memory.
