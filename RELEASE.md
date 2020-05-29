# Release

This document, the release process as well as the versioning strategy for the DCGM exporter.
In the future this document will also contain information about the go bindings.

## Versioning

The DCGM container posses three major components:
- The DCGM Version (e.g: 1.17.3)
- The Exporter Version (e.g: 2.0.0)
- The platform of the container (e.g: ubuntu18.04)

The overall version of the DCGM container has four forms:
- The long form: `${DCGM_VERSION}-${EXPORTER_VERSION}-${PLATFORM}`
- The short form: `${DCGM_VERSION}`
- The latest tag: `latest`
- The commit form: `${CI_COMMIT_SHORT_SHA}` only available on the gitlab registry

The long form is a unique tag that once pushed will always refer to the same container.
This means that no updates will be made to that tag and it will always point to the same container.

The short form refers to the latest EXPORTER_VERSION with the platform fixed to ubuntu18.04.
The latest tag refers to the latest short form (i.e: latest DCGM_VERSION and EXPORTER_VERSION).

Note: We do not maintain multiple version branches.

## Releases

Release of newer versions is done on demand and does not follow DCGM's release cadence.
Though it is very likely that when a new version of DCGM comes out a new version of the exporter will be released.

All commit to the master branch generates an image on the gitlab registry.
Tagging a version will push an image to the nvidia/dcgm-exporter repository on the Dockerhub
