# exec

Thin context-aware wrapper around `os/exec`. All command construction
goes through this package so call sites are easy to audit.

## Policy

- Callers MUST pass a `context.Context` with a timeout. The context
  bounds how long the subprocess may run; without a timeout, a hung
  subprocess hangs the daemon.
- Command names and arguments MUST be whitelisted at the call site,
  never derived from user input or untrusted config. The `gosec G204`
  suppression on `RealExec.CommandContext` is intentional and reviewed
  because the abstraction itself is generic by design — safety lives
  at the call sites.

## Caller checklist

When adding a new call site:

1. Pass `ctx, cancel := context.WithTimeout(parent, T)` and `defer cancel()`.
2. Pin `name` to a constant string or an `exec.LookPath` result, never
   a value read from CLI flags / env vars / files.
3. Pin `arg...` to constants or known-safe values (numeric IDs, etc.).
