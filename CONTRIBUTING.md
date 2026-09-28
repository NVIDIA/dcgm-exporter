# Contribute to the DCGM-Exporter Project

Want to hack on the NVIDIA DCGM-Exporter Project? Awesome!
We only require you to sign your work, the below section describes this!

## Sign your work

The sign-off is a simple line at the end of the explanation for the patch. Your
signature certifies that you wrote the patch or otherwise have the right to pass
it on as an open-source patch. The rules are pretty simple: if you can certify
the below (from [developercertificate.org](http://developercertificate.org/)):

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.
1 Letterman Drive
Suite D4700
San Francisco, CA, 94129

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.

Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

Then you just add a line to every git commit message:

    Signed-off-by: Joe Smith <joe.smith@email.com>

Use your real name (sorry, no pseudonyms or anonymous contributions.)

If you set your `user.name` and `user.email` git configs, you can sign your
commit automatically with `git commit -s`.

## Agent-assisted development

This repository includes guidance for AI-assisted work:

- `AGENTS.md` and scoped `AGENTS.md` files describe repository conventions,
  feature-planning guidance, owning surfaces, and validation gates.
- `llms.txt` is the machine-readable contract for metric CSV, CLI/env, Helm,
  Docker/package, and validation guidance.
- `.codex/` and `.cursor/` contain tool-specific notes, commands, rules, and
  review checklists. They are guidance only; they do not grant permissions or
  install automatic hooks.

Agents and humans should treat the current code, tests, Makefile, CI config,
`hack/versions.env`, metric CSV files, and Helm chart as the source of truth.

## Bumping a dependency

All pinned versions live in a single file: `hack/versions.env`. This covers
the Go toolchain, Go tools (golangci-lint, goimports, gofumpt, gotestsum,
gocover-cobertura), uv, container base image tags (CUDA, distroless, ubuntu),
e2e helper images, and the DCGM/exporter release version.

### Workflow

1. **See what's outdated:**

   ```bash
   make check-versions
   ```

   This queries upstream sources (go.dev, GitHub releases) and shows a table
   of current vs. latest versions. Requires network; optionally set
   `GITHUB_TOKEN` to avoid rate limits.

2. **Bump a version** — edit `hack/versions.env` directly, or use the apply
   subcommand for auto-checkable packages:

   ```bash
   make check-versions-apply       # updates versions.env
   ```

3. **Propagate to derived files:**

   ```bash
   make sync-versions
   ```

   This rewrites static files that can't read `versions.env` at runtime:
   the `go.mod` toolchain directive, Dockerfile `FROM` lines, `.gitlab-ci.yml`
   variables and `.gitlab/ci/dependencies.yml` Go SHA256s when those files are
   present, the Helm chart, raw YAML artifacts, and fenced version examples in
   the READMEs.

4. **Verify nothing drifted:**

   ```bash
   make validate-versions
   ```

   This exits non-zero if any derived file is out of sync. It also refreshes
   the pinned Go tarball SHA256s from `dl.google.com`, so it requires network
   access. CI runs this automatically on every push.

### Go module dependencies

Go module dependencies (in `go.mod`) are **not** managed by `versions.env`.
Bump them with the standard Go toolchain:

```bash
go get google.golang.org/grpc@v1.79.3
go mod tidy
```

### Package revisions

`PACKAGE_REVISION` in `hack/versions.env` is a three-digit, package-only
revision. Package operations derive `PACKAGE_VERSION` as
`<EXPORTER_VERSION>.<PACKAGE_REVISION>`; binaries, containers, Helm artifacts,
and the public application version continue to use `EXPORTER_VERSION`.

Start each new exporter version at `001`. If package bytes must be rebuilt
without changing the exporter version, increment the revision to `002`, `003`,
and so on. Existing package artifacts are immutable: an upload retry is allowed
only when its checksum matches. Never derive `PACKAGE_REVISION` from a Git tag,
pipeline ID, or an external release-system identifier. `PACKAGE_RELEASE`
remains the separate DEB/RPM release field and defaults to `1`.

### Prerequisites

- **uv** (Python runner): `make install-uv` installs the pinned version. All
  Python scripts in this repo use `#!/usr/bin/env -S uv run --script` so
  dependencies are installed automatically on first run.

### Intentional exceptions

- `.github/workflows/go.yml` pins `go-version: 1.24` (major.minor only) so
  GitHub Actions uses the latest patch automatically. This file is not touched
  by `make sync-versions`.
