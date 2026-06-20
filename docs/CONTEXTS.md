# Context architecture — dcgm-exporter

Audience: contributors adding or reviewing code that does I/O (network, file, subprocess, channel-wait).

## Why context

dcgm-exporter is a long-running daemon. It needs a single, signal-cancellable root `context.Context` that propagates through every I/O path so that a shutdown signal (`SIGTERM`), a Prometheus scrape timeout, or a hung gRPC call can abort cleanly instead of waiting on per-call timeouts. Standard Go idiom — nothing exporter-specific.

## Where the root is created

`cmd/dcgm-exporter/main.go`:

```go
func main() {
    // Root context, cancelled by SIGINT or SIGTERM. SIGHUP keeps flowing
    // through SignalSource for hot reload.
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    app := cmd.NewApp(BuildVersion)
    if err := app.RunContext(ctx, os.Args); err != nil {
        slog.Error(err.Error())
        os.Exit(1)
    }
}
```

`app.RunContext(ctx, …)` plumbs `ctx` into urfave/cli's `c.Context`, which the daemon's action handler reads and threads onward.

## Signal handling — two channels by design

dcgm-exporter handles three signals; they are intentionally routed differently:

| Signal | Mechanism | Semantics |
| --- | --- | --- |
| `SIGINT` | `signal.NotifyContext` → cancels root ctx | Shutdown |
| `SIGTERM` | `signal.NotifyContext` → cancels root ctx | Shutdown |
| `SIGHUP` | `cmd.SignalSource` channel | Hot reload (re-read counters CSV, rebuild registry, atomic swap — daemon stays up) |

This split is deliberate. Shutdown and reload are different events; conflating them in one mechanism would either break reload (operators rely on `kill -HUP $(pidof dcgm-exporter)` for zero-downtime config changes) or weaken shutdown cancellation. Do not "fix" this by folding SIGHUP into the ctx.

## Propagation tree

```
cmd/dcgm-exporter/main.go
  ctx, stop = signal.NotifyContext(Background, SIGINT, SIGTERM)
    │
    └── app.RunContext(ctx, os.Args)
          │
          └── c.Context = ctx                          (urfave/cli plumbs this)
                │
                └── action(c)
                      │
                      └── stdout.Capture(c.Context, …)
                            │
                            └── startDCGMExporter(c)
                                  │
                                  └── StartDCGMExporterWithSignalSource(c, sigSource)
                                        │
                                        ├── ctx := c.Context                  (root for the daemon)
                                        │     │
                                        │     ├── buildRegistry(ctx, …)
                                        │     ├── getCounters(ctx, …)          ConfigMap reader honours ctx
                                        │     ├── metricsServer.Run(ctx, stop)
                                        │     │     │
                                        │     │     └── render(ctx, …)
                                        │     │           └── transformer.Process(ctx, …)
                                        │     │                 └── PodMapper.listPods(ctx, conn)
                                        │     │                       └── client.List(ctx, …)   gRPC respects parent
                                        │     │
                                        │     └── draManager = NewDRAResourceSliceManager(ctx)
                                        │           └── factory.Start(ctx.Done())   informer respects parent
                                        │
                                        ├── watcherCtx, _ = context.WithCancel(ctx)
                                        │     ├── runWatcher(watcherCtx, fileWatcher, onChange)
                                        │     ├── runGPUWatcher(watcherCtx, gpuWatcher, onChange)
                                        │     ├── hotReload(watcherCtx, …)              triggered by SIGHUP
                                        │     └── handleGPUTopologyChange(watcherCtx, …)
                                        │
                                        └── sigSource.Signals() loop              SIGHUP → hotReload
```

Three kinds of arrows in this tree:

- **Inherited** (`c.Context = ctx`, `ctx := c.Context`): the root flows down unchanged.
- **`context.WithCancel(parent)`** (`watcherCtx`): a child whose cancel is wired to a specific subsystem's shutdown path.
- **`context.WithTimeout(parent, …)`** (per-call): a child with a per-operation deadline. The parent's cancellation still propagates.

## Conventions for new code

1. **Any function that does I/O** — network, file, subprocess, channel-wait beyond a few ms — MUST take `ctx context.Context` as its first parameter, name it `ctx`, and honour cancellation (check `ctx.Err()` between blocking operations; pass `ctx` to downstream calls).
2. **Never write `context.Background()` in production code.** The only exceptions are documented in §"Acceptable Background() sites" below. Tests are excluded — they use `context.Background()` as a sentinel for "no parent" all the time, which is fine.
3. **Never write `context.TODO()` in production code, period.** `context.TODO()` is a marker for "I haven't decided what context to use yet" — that's a bug.
4. **Subprocess invocations** go through `internal/pkg/exec`. See its `README.md` for the wrapper policy. The wrapper enforces `CommandContext`, so subprocesses always honour cancellation.
5. **Per-call deadlines** wrap the inherited ctx with `context.WithTimeout(parent, …)`. Do not start the deadline from `context.Background()`; that orphans the call from the root and breaks shutdown.
6. **gRPC, Kubernetes, HTTP clients** all accept a `context.Context`. Pass it through. They handle the cancellation internally.

## Acceptable `context.Background()` sites

After this refactor, the **only** production sites that use `context.Background()` are:

| Site | Why |
| --- | --- |
| `cmd/dcgm-exporter/main.go` (the root) | API requirement — `signal.NotifyContext(parent, …)` needs a parent; `context.Background()` is the convention for the process root. |
| `internal/pkg/devicewatcher/device_watcher.go:82,405` | Inside `Cleanup` / inline cleanup callback. No caller ctx is in scope. `slog.LogAttrs` uses the ctx only for handler attribute extraction, not for cancellation, so `context.Background()` is harmless here. |

Any other `context.Background()` in production code is a bug. The static analysis pipeline (`golangci-lint` with `contextcheck` enabled) gates new offenders.

## Testing patterns

Match the repo idiom: table-driven, stretchr/testify, `t.Run(tc.name, …)`. Cover positive, negative, boundary, and corner cases.

### Recipe

```go
func TestThingHonoursContext(t *testing.T) {
    cases := []struct {
        name        string
        prepCtx     func() (context.Context, context.CancelFunc)
        wantErrKind error          // nil, context.Canceled, context.DeadlineExceeded
        wantTimely  bool           // returns within a tight bound after cancel
    }{
        {
            name: "positive: fresh ctx, runs to completion",
            prepCtx: func() (context.Context, context.CancelFunc) {
                return context.WithCancel(context.Background())
            },
            wantErrKind: nil,
            wantTimely:  true,
        },
        {
            name: "negative: pre-cancelled ctx aborts immediately",
            prepCtx: func() (context.Context, context.CancelFunc) {
                ctx, cancel := context.WithCancel(context.Background())
                cancel()
                return ctx, func() {}
            },
            wantErrKind: context.Canceled,
            wantTimely:  true,
        },
        {
            name: "boundary: cancel mid-call propagates",
            prepCtx: func() (context.Context, context.CancelFunc) {
                ctx, cancel := context.WithCancel(context.Background())
                go func() { time.Sleep(50 * time.Millisecond); cancel() }()
                return ctx, func() {}
            },
            wantErrKind: context.Canceled,
            wantTimely:  true,
        },
        {
            name: "corner: deadline shorter than work",
            prepCtx: func() (context.Context, context.CancelFunc) {
                return context.WithTimeout(context.Background(), 50*time.Millisecond)
            },
            wantErrKind: context.DeadlineExceeded,
            wantTimely:  true,
        },
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            ctx, cancel := tc.prepCtx()
            defer cancel()

            start := time.Now()
            err := thingUnderTest(ctx)
            elapsed := time.Since(start)

            if tc.wantErrKind == nil {
                assert.NoError(t, err)
            } else {
                assert.ErrorIs(t, err, tc.wantErrKind)
            }
            if tc.wantTimely {
                assert.Less(t, elapsed, 150*time.Millisecond,
                    "ctx cancellation did not propagate")
            }
        })
    }
}
```

### Existing examples

- `internal/pkg/watcher/gpu_test.go` — ctx-cancel test for the GPU bind-unbind watcher.
- `internal/pkg/stdout/capture_test.go` — ctx wrapping inside `stdout.Capture`.
- `pkg/cmd/app_test.go` — `TestActionUsesCliContext` for the daemon entry-point chain.

## How to verify your change

```bash
make test-main          # unit tests, no GPU needed
make lint               # golangci-lint with contextcheck enabled

# Manual smoke (any host, no GPU needed for shutdown path):
./dcgm-exporter &
sleep 1
kill -TERM $!           # exits within ~1 second
```

For the integration suite (which needs a real GPU + DCGM):

```bash
make test-integration
```

## Maintenance

When you add a new context-aware function:

1. Put `ctx context.Context` as the first parameter.
2. Pass `ctx` (or a `WithTimeout(ctx, …)` / `WithCancel(ctx)` child) to every downstream call that accepts one.
3. If your function is long-running, sprinkle `if err := ctx.Err(); err != nil { return err }` between blocking operations.
4. Add a table-driven test from the recipe above.
5. Run `make lint` — `contextcheck` will flag any context discontinuity.
