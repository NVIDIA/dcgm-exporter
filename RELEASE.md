# Release artifacts

This document describes the names and versions of published DCGM Exporter
artifacts.

## Version mapping

`EXPORTER_VERSION` and `DCGM_VERSION` come from `hack/versions.env`. Release
candidate (`.rc`) tag pipelines set `PACKAGE_VERSION` to
`<EXPORTER_VERSION>.<CI_PIPELINE_IID>`, where `CI_PIPELINE_IID` is the numeric
CI pipeline number.

<!-- sync:release-artifact-example:start -->
For `EXPORTER_VERSION=4.9.0` and `CI_PIPELINE_IID=1234`, the package artifacts are:

```text
dcgm_exporter-linux-x86_64-4.9.0.1234.tar.gz
dcgm_exporter-linux-sbsa-4.9.0.1234.tar.gz
RPM/DEB package version: 4.9.0.1234-1
```
<!-- sync:release-artifact-example:end -->

The version mapping is implemented in these repository files:

- `hack/versions.env` defines `EXPORTER_VERSION` and `DCGM_VERSION`.
- Release candidate (`.rc`) tag pipelines derive `PACKAGE_VERSION`.
- `Taskfile.yml` adds `PACKAGE_VERSION` to package archive names.
- `hack/package/test.sh` validates the RPM/DEB version and package release.

Binary and Helm versions use `EXPORTER_VERSION`. The released distroless
container has these public tags:

<!-- sync:distroless-tag-examples:start -->
- `${EXPORTER_VERSION}`, the tag to use (for example, `4.9.0`)
- `latest` as the moving alias
- `${DCGM_VERSION}-${EXPORTER_VERSION}-distroless` and
  `${DCGM_VERSION}-${EXPORTER_VERSION}`, legacy aliases kept for existing
  references (for example, `4.7.0-4.9.0-distroless`)
<!-- sync:distroless-tag-examples:end -->
