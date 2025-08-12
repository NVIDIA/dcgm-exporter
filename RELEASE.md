# Release

This documents the release process as well as the versioning strategy for the DCGM exporter.

## Versioning

The DCGM container has three major components:
- The DCGM Version (e.g: 4.2.3)
- The Exporter Version (e.g: 4.1.1)
- The platform of the container (e.g: ubuntu22.04)

The overall version of the DCGM container has three forms:
- The long form: `${DCGM_VERSION}-${EXPORTER_VERSION}-${PLATFORM}`
- The short form: `${DCGM_VERSION}`
- The latest tag: `latest`

The long form is a unique tag that once pushed will always refer to the same container.
This means that no updates will be made to that tag and it will always point to the same container.

The short form refers to the latest EXPORTER_VERSION with the platform fixed to ubuntu22.04.
The latest tag refers to the latest short form (i.e: latest DCGM_VERSION and EXPORTER_VERSION).

Note: We do not maintain multiple version branches. The Exporter functions with the latest go-dcgm bindings.

## Releases

Newer versions are released on demand but tend to follow DCGM's release cadence.
