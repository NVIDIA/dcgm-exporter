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

Agents and humans should treat the current code, tests, Taskfile.yml, CI config,
`hack/versions.env`, metric CSV files, and Helm chart as the source of truth.

## Build binaries, images, and packages

Use the root Taskfile.yml for common development commands. Run `bootstrap.sh`
once to install the required Task version. If it asks you to update `PATH`,
do so and open a new shell. Then install the locked tools and list the tasks.
Task runs Bazel where needed.

```bash
./bootstrap.sh
task deps:install
task --list
```

```bash
# Build binaries for the current architecture.
task build:binary
task build:e2e

# Build the distroless runtime image; task build and task build:image:distroless do the same.
task build
task build:image:distroless

# Load the image for the current architecture into the local Docker daemon,
# then check its architecture, labels and that `--version` runs.
task build:image:local
task build:image:verify

# Build package archives for both architectures, or select one architecture.
task build:package
task build:package:amd64
task build:package:arm64
```

`task build:binary`, the image tasks, and the package tasks all get the
exporter binary from Bazel. Dockerfiles and package scripts assemble that
binary; they do not run a second Go compilation path.

## Vulnerability scans

```bash
task validate:vulns
task validate:vulns:image -- --image "$IMAGE_REF" --arch amd64
```

Both tasks write reports under `dist/security/`. The source scan fails on any
reachable finding; the image scan fails on fixable High or Critical package
findings, on any vulnerability govulncheck finds in the exporter binary, and on
file metadata violations.
Pass `-- --help` to either task for its options.

## Bumping a dependency

`hack/versions.env` is the canonical source for non-module versions, checksums,
Action SHAs, image tags, and image manifest digests. `hack/versions.py`
defines the immutable schema, freshness source or manual rationale, update
policy, companion values, cross-key relationships, upstream resolvers, and
transactional commands. Duplicate, missing, unknown, empty, or malformed keys
are errors.

Go module versions remain exclusively in `go.mod` and `go.sum`. Compatibility
floors, schema versions, and values used only as test fixtures stay with their
owning code or test.

### Version command

Install the locked development tools with `task deps:install`. All version
maintenance then uses the same `hack/versions.py` command:

```bash
task deps:install
task deps:update
task deps:update:strict
task deps:update:apply
task deps:sync
task deps:lock
task deps:sync:check
task deps:check:all
```

`task deps:update` queries upstream releases, tags, Action refs, artifact
checksums, OCI manifests, and Go's native module update data without modifying
the worktree. It reports both graph changes selected by
`go get -u -t ./...` and newer transitive releases that Go leaves constrained
by the selected graph. It returns `0` for a complete report. The strict form
returns `1` for applicable updates and `2` when a source is incomplete or
fails.

OCI authentication, pagination, and redirects stay on the configured registry's
HTTPS origin. Docker Hub also permits its token service at `auth.docker.io`.
The Docker Hub tags API stays on `hub.docker.com`. Other destinations, embedded
URL credentials, and HTTPS downgrades fail dependency discovery. A registry
that requires a separate token service needs an explicit trusted origin in
the OCI resolver before it can be used.

The checker honors retry and rate-limit headers. Set `GITHUB_TOKEN` to increase
GitHub API limits. If the unauthenticated GitHub API is rate-limited, release
discovery follows GitHub's public latest-stable-release redirect so prereleases
remain excluded. Tag and Action commit discovery falls back to Git tag refs.

`task deps:update:apply` resolves the complete update set, including official
artifact checksums, Action commits, and OCI manifest digests, validates the
proposed registry, upgrades the compatible Go module graph with
`go get -u -t ./...` and `go mod tidy`, preflights all derived files, and
commits one transactional write plan. Network, Go, validation, anchor, or write
failures leave the worktree unchanged. Major-version module path migrations and
manual-policy pins still require maintainer review.

`task deps:sync` copies versions from `hack/versions.env` to files that need
them. It does not check for newer releases. After changing a Mise tool version,
run `task deps:lock` to update the lock file for each platform. Sync checks
every location before writing and restores the original files if a write fails.
`task deps:sync:check` checks for drift without writing. The version tool
exits `0` when the files match, `1` when they differ, and `2` when an input or
file is invalid; Task reports any failure as its own exit status unless run
with `--exit-code`. `task deps:check:all` runs `task deps:test` and then
`task deps:sync:check`, as CI does.

The root Taskfile.yml and repository shell scripts load `versions.env`.
Consumers that cannot load it before evaluation remain synchronized: `go.mod`,
Dockerfile defaults and `FROM` references, GitHub Action refs, Helm and raw
manifests, devcontainer metadata, and exact documentation defaults.
`task deps:sync` also applies updates from any `version-sync-targets.py` in a
top-level `*/ci/` directory, which defines `updates(values)` returning
`AnchoredUpdate` items.

Image pins in `hack/versions.env` keep a readable tag and the multi-platform
manifest digest. When a tag is intentionally moving, such as `latest`, the
digest is still refreshed and reviewed. Do not substitute an
architecture-specific child-manifest digest.
Every external image repository is also declared in `hack/versions.env`.
The CI image source defaults to `docker.io`. Override `CI_IMAGE_MIRROR` with a
reachable mirror when needed; the mirror hostname is CI configuration, not
version data. The synchronizer derives each image path from its canonical
Docker Hub repository in `versions.env`; no duplicate mirror repository is
maintained.
Repository locations must not be embedded independently in the Python tooling.

The rootless Docker-in-Docker service is the one tag-only exception: CI runners
can allow privileges only for an exact service tag, and a digest-qualified
reference would start without the privileges rootlesskit requires.
`CI_DIND_IMAGE_MIRROR` selects the registry for that service.
Other external image references use an immutable digest, usually rendered as
`repository:tag@sha256:...`. The distroless base, helper, and package-validator
UBI8 `FROM` references use `repository@sha256:...`; their readable tags remain
in `hack/versions.env`.

Manual-policy entries are not hidden from freshness reports. CUDA/Ubuntu
combinations, compatibility matrices, and DCGM/exporter release identity appear
with the rationale that requires a maintainer decision.
BuildKit is also a compatibility pin. It is held on purpose and reviewed by
hand.

CI runs version tests and validation on every pipeline. Strict freshness checks
run when a change touches version values or tooling, and on a weekly schedule;
they are advisory, so upstream drift does not block pipelines.

### Release artifact versions

`EXPORTER_VERSION` in `hack/versions.env` is the base version for published
artifacts. Candidate CI derives `PACKAGE_VERSION` as
`<EXPORTER_VERSION>.<CI_PIPELINE_IID>`. See [RELEASE.md](RELEASE.md) for the
resulting archive, package, Helm, and container versions.

### Prerequisites

- **Python 3.12+** runs the version command.
- **uv** installs the dependencies declared by the PEP 723 metadata in
  `hack/versions.py` as needed. `task deps:install` installs the pinned uv
  version; the script dependencies are resolved by uv at execution time.

### Intentional exceptions

- The `go` directive is rendered from `GO_VERSION`. Builds and CI install that
  same toolchain; there is no separate language-floor pin.
- `.github/workflows/go.yml` runs in the devcontainer, where Mise installs the
  locked Go toolchain; its Action revisions are pinned to full commit SHAs.
- Go module versions, compatibility floors, schema versions, and test-fixture
  values are intentionally not duplicated into `versions.env`.
