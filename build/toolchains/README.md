# Bazel C toolchain

DCGM Exporter is not a pure-Go binary. `go-dcgm` and `go-nvml` use CGO, so
`rules_go` needs a C compiler and linker when it builds the exporter and several
tests.

## Why this repository uses Zig

Using the compiler installed on a CI runner would make the result depend on the
runner image. It would also require separate native compiler setup for amd64 and
arm64. The pinned Zig downloads in `MODULE.bazel` provide the same compiler on
both runner architectures and can target either output architecture.

The compiler entry points select `x86_64-linux-gnu.2.28` or
`aarch64-linux-gnu.2.28`. The suffix makes the package's glibc 2.28 compatibility
limit part of the compiler target instead of relying on whichever glibc happens
to be installed on the runner.

`BUILD.bazel` registers all four execution/target combinations:

- amd64 output on an amd64 runner;
- arm64 output on an amd64 runner;
- amd64 output on an arm64 runner; and
- arm64 output on an arm64 runner.

## Why the compiler wrapper exists

`zig_cc_wrapper.sh` adapts Zig to the arguments produced by Bazel and
`rules_go`:

- it finds the pinned Zig executable in Bazel's execution root;
- it confines Clang's GCC discovery to the Zig archive so host GCC installations
  cannot affect compiler selection or driver diagnostics;
- it rewrites absolute Zig paths in dependency files to execution-root-relative
  paths so sandboxed and cached actions do not depend on a machine-specific
  directory; and
- for the CGO partial-link action, it preserves the requested unresolved-symbol
  behavior while removing Zig's eager-binding linker option. That partial link
  intentionally leaves symbols for Go's final link step.

The completed product is checked for architecture and its newest required glibc version by
`hack/bazel/stage-artifacts.sh`.

## When this can be removed

This toolchain can be removed if DCGM Exporter no longer uses CGO, or if another
registered Bazel C toolchain provides both architectures, the glibc 2.28 limit,
and reproducible behavior on amd64 and arm64 runners. Passing pure-Go tests is
not sufficient evidence because those tests do not invoke the C toolchain.
