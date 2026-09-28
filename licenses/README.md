# Third-party notice generation

The root `THIRD_PARTY_NOTICES` file contains the legal materials supplied by third-party Go modules compiled into the production `./cmd/dcgm-exporter` binary. It ships beside `LICENSE` in published standalone binary payloads. The repository's `go.mod` and `go.sum` files are the module-version source of truth.

Libraries supplied dynamically by the target host are outside the standalone binary payload.

Regenerate it after changing the production Go dependency closure:

```bash
make third-party-notices
```

`make check-third-party-notices` regenerates the file in a temporary location and fails if the checked-in file differs. It is part of `make validate`.

The Go collector disables workspace overrides, uses `go list -deps` for both shipped Linux architectures (`amd64` and `arm64`), verifies the module cache against `go.sum`, and preserves the discovered legal-file bytes. It excludes NVIDIA-authored modules and the Go standard library. A third-party module without a top-level legal file is a generation failure.

The production distroless image generates one `/licenses/THIRD_PARTY_NOTICES` file per architecture from:

- the checked-in Go notice;
- the third-party notice supplied by the installed, version-pinned DCGM package; and
- package copyright files for package-owned runtime files actually staged from the Ubuntu helper.

The base image keeps its existing notices in their original locations. The exporter repository does not vendor DCGM license files. The image build fails if its Go notice, DCGM notice, or an added runtime package notice is missing. CI extracts the generated image notice as a review artifact.
