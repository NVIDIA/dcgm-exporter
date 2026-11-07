# Bazel implementation details

Run Bazel through Task. The scripts here choose the Bazel version installed by
Mise, supply build metadata, and copy Bazel outputs for Docker and packages.
They do not compile Go outside Bazel.

Use these commands from a checkout; CI uses the same commands:

```bash
./bootstrap.sh
task deps:install
task bazel:fetch
BAZEL_TARGETS=//cmd/dcgm-exporter:dcgm-exporter task bazel:build
task test:unit
```

`BAZEL_PLATFORM_CONFIG` defaults to the host architecture and
`BAZEL_TARGETS` defaults to targets that do not require manual setup.
`bazel:fetch`, `bazel:build`, and `test:unit` (alias `bazel:test`) accept
`BAZEL_FETCH_FLAGS`, `BAZEL_BUILD_FLAGS`, and `BAZEL_TEST_FLAGS` when a caller
needs to extend their respective command. `task test:unit COVERAGE=true
REPORTS=true` runs `bazel coverage` and writes the JUnit, coverage and Sonar
reports, as CI does.

## `run.sh`

Task and some CI checks call this script. It runs the Bazel version installed
by Mise, which `task deps:sync` keeps aligned with `.bazelversion`, and loads
`DCGM_EXPORTER_BAZEL_CACHE_RC` (default `.workspace/bazel-cache.bazelrc`) when
that file exists. The checked-in `.bazelrc` loads the ignored `.bazelrc.local`;
`task dev:go-dcgm:use` writes a local `--override_module` setting there, and CI
uses the same file.

The launcher does not inspect `go.work`, clone repositories, generate BUILD
files, or infer a dependency override. Those choices are explicit in the local
or CI Bazel configuration.

Normal `task bazel:fetch`, `task bazel:build`, and `task test:unit` commands
skip targets that require manual setup, so they can fetch dependencies, build,
and test without optional runtime files.

## `stage-artifacts.sh`

Docker and package workflows consume regular files, but Bazel outputs live
under configuration-dependent paths. This script builds the requested Bazel
targets, finds their outputs with `cquery`, checks the CPU architecture,
release version, and glibc limit, then copies them to a staging directory.
Docker and package builds use these staged files.

The script rejects staging paths it does not own. Set
`STAGED_ARTIFACT_ROOT` only to a disposable location; default outputs live
under `dist/staged/` and are ignored by Git.
