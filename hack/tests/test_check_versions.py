from __future__ import annotations

import sys
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest import mock

HACK_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HACK_DIR))
sys.dont_write_bytecode = True

try:
    import requests
    import yaml  # noqa: F401

    import packaging  # noqa: F401
except ModuleNotFoundError as error:
    raise unittest.SkipTest(
        f"version checker dependencies are unavailable: {error}"
    ) from error

from version_tool import cli, pins, upstream


class FakeResponse:
    """Supply the response fields consumed by the HTTP client without a network call."""

    def __init__(
        self,
        *,
        status_code=200,
        headers=None,
        content=b"",
        url="https://example.test/value",
    ):
        """Store the status, headers, payload, and final URL for one fake response."""

        self.status_code = status_code
        self.headers = headers or {}
        self.content = content
        self.url = url


class QueueSession:
    """Record GET arguments and replay queued responses or transport failures."""

    def __init__(self, responses):
        """Copy the response queue and start an empty request history."""

        self.responses = list(responses)
        self.calls = []

    def get(self, url, **kwargs):
        """Record one GET and return or raise the next queued outcome."""

        self.calls.append((url, kwargs))
        response = self.responses.pop(0)
        if isinstance(response, BaseException):
            raise response
        return response


class StaticClient:
    """Replay wrapped HTTP responses directly to upstream resolvers."""

    def __init__(self, responses):
        """Copy resolver responses and start an empty call history."""

        self.responses = list(responses)
        self.calls = []

    def get(self, url, **kwargs):
        """Record resolver request arguments and consume its next response."""

        self.calls.append((url, kwargs))
        return self.responses.pop(0)


class HTTPTests(unittest.TestCase):
    """Check HTTP retries, thread ownership, pagination, and authentication offline."""

    def test_default_sessions_are_owned_by_worker_threads(self) -> None:
        """Use a barrier to require two concurrent workers to create separate sessions."""

        barrier = threading.Barrier(2)

        class BarrierSession:
            """Hold each fake GET until both worker threads have entered their sessions."""

            def get(self, url, **_kwargs):
                """Meet the other worker at the barrier and return this request URL as data."""

                barrier.wait(timeout=5)
                return FakeResponse(content=url.encode(), url=url)

        sessions = [BarrierSession(), BarrierSession()]
        with mock.patch.object(
            upstream.requests,
            "Session",
            side_effect=sessions,
        ) as session_factory:
            client = upstream.HTTPClient()
            with ThreadPoolExecutor(max_workers=2) as executor:
                results = list(
                    executor.map(
                        client.get,
                        ("https://example.test/one", "https://example.test/two"),
                    )
                )

        self.assertEqual(session_factory.call_count, 2)
        self.assertEqual(
            {response.text for response in results},
            {"https://example.test/one", "https://example.test/two"},
        )

    def test_retry_after_header_is_honored(self) -> None:
        """Record the server-requested delay before a throttled request succeeds."""

        session = QueueSession(
            [
                FakeResponse(status_code=429, headers={"Retry-After": "7"}),
                FakeResponse(status_code=200, content=b"ok"),
            ]
        )
        sleeps = []
        client = upstream.HTTPClient(
            session=session,
            sleep=sleeps.append,
        )
        response = client.get("https://example.test/retry")
        self.assertEqual(response.content, b"ok")
        self.assertEqual(sleeps, [7.0])

    def test_transient_connection_error_is_retried(self) -> None:
        """Record one backoff delay and recover from a connection reset."""

        session = QueueSession(
            [
                requests.ConnectionError("connection reset"),
                FakeResponse(status_code=200, content=b"ok"),
            ]
        )
        sleeps = []
        client = upstream.HTTPClient(
            session=session,
            sleep=sleeps.append,
        )
        response = client.get("https://example.test/retry")
        self.assertEqual(response.content, b"ok")
        self.assertEqual(sleeps, [1.0])

    def test_transport_failure_exhaustion_is_an_environment_error(self) -> None:
        """Require three failed attempts and two recorded delays before reporting failure."""

        session = QueueSession(
            [
                requests.Timeout("timed out"),
                requests.Timeout("timed out"),
                requests.Timeout("timed out"),
            ]
        )
        sleeps = []
        client = upstream.HTTPClient(
            session=session,
            sleep=sleeps.append,
        )
        with self.assertRaisesRegex(
            pins.VersioningError,
            "request failed after 3 attempts",
        ):
            client.get("https://example.test/retry")
        self.assertEqual(sleeps, [1.0, 2.0])

    def test_github_tag_pagination(self) -> None:
        """Find the newest tag on the second page rather than stopping at the first."""

        first = upstream.HTTPResponse(
            200,
            {},
            upstream.json.dumps(
                [{"name": f"v0.{index}.0"} for index in range(100)]
            ).encode(),
            "https://api.github.test/tags?page=1",
        )
        second = upstream.HTTPResponse(
            200,
            {},
            b'[{"name":"v0.101.0"}]',
            "https://api.github.test/tags?page=2",
        )
        client = StaticClient([first, second])
        latest = upstream._github_latest_tag(client, "owner/repo:v0.")
        self.assertEqual(latest, "v0.101.0")
        self.assertEqual(len(client.calls), 2)

    def test_annotated_github_tag_resolves_commit(self) -> None:
        """Follow an annotated tag object to its commit SHA."""

        tag_object = upstream.HTTPResponse(
            200,
            {},
            b'{"object":{"type":"tag","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}',
            "https://api.github.test/ref",
        )
        commit_object = upstream.HTTPResponse(
            200,
            {},
            b'{"object":{"type":"commit","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}',
            "https://api.github.test/tag",
        )
        client = StaticClient([tag_object, commit_object])
        self.assertEqual(
            upstream._github_tag_sha(client, "owner/repo", "v1.2.3"),
            "b" * 40,
        )

    def test_github_release_falls_back_to_stable_release_redirect(self) -> None:
        """Use the stable release redirect when the unauthenticated API is limited."""

        client = mock.Mock()
        client.get.side_effect = [
            pins.VersioningError(
                "GET https://api.github.com/example: HTTP 403"
            ),
            upstream.HTTPResponse(
                200,
                {},
                b"",
                "https://github.com/owner/repo/releases/tag/v7.0.0%2Bstable",
            ),
        ]
        with (
            mock.patch.dict(upstream.os.environ, {"GITHUB_TOKEN": ""}, clear=False),
            mock.patch.object(
                upstream,
                "_git_remote_tags",
                # Git refs cannot reveal that GitHub marks the higher tag prerelease.
                return_value={
                    "v7.0.0+stable": "a" * 40,
                    "v8.0.0": "b" * 40,
                },
            ) as git_remote_tags,
        ):
            self.assertEqual(
                upstream._github_latest_release(client, "owner/repo"),
                "v7.0.0+stable",
            )
        git_remote_tags.assert_not_called()

    def test_github_release_rejects_invalid_fallback_redirect(self) -> None:
        """Reject a fallback URL that does not identify a release tag."""

        client = mock.Mock()
        client.get.side_effect = [
            pins.VersioningError(
                "GET https://api.github.com/example: HTTP 403"
            ),
            upstream.HTTPResponse(200, {}, b"", "https://github.com/owner/repo"),
        ]
        with mock.patch.dict(
            upstream.os.environ, {"GITHUB_TOKEN": ""}, clear=False
        ):
            with self.assertRaisesRegex(
                pins.VersioningError,
                "did not redirect to a tag",
            ):
                upstream._github_latest_release(client, "owner/repo")

    def test_github_ref_falls_back_to_peeled_git_tag(self) -> None:
        """Use the peeled Git commit when the tag API is limited."""

        client = mock.Mock()
        client.get.side_effect = pins.VersioningError(
            "GET https://api.github.com/example: HTTP 403"
        )
        with (
            mock.patch.dict(upstream.os.environ, {"GITHUB_TOKEN": ""}, clear=False),
            mock.patch.object(
                upstream,
                "_git_remote_tags",
                return_value={"v1.2.3": "c" * 40},
            ),
        ):
            self.assertEqual(
                upstream._github_tag_sha(client, "owner/repo", "v1.2.3"),
                "c" * 40,
            )

    def test_git_remote_tags_prefers_peeled_annotated_commit(self) -> None:
        """Prefer the commit reference over the annotated tag object SHA."""

        tag_object = "d" * 40
        commit = "e" * 40
        completed = mock.Mock(
            returncode=0,
            stdout=(
                f"{tag_object}\trefs/tags/v1.2.3\n{commit}\trefs/tags/v1.2.3^{{}}\n"
            ),
            stderr="",
        )
        upstream._git_remote_tags.cache_clear()
        with mock.patch.object(upstream.subprocess, "run", return_value=completed):
            self.assertEqual(
                upstream._git_remote_tags("owner/annotated-repo")["v1.2.3"],
                commit,
            )

    def test_docker_hub_pagination(self) -> None:
        """Follow the next-page URL and retain tags from both pages."""

        first = upstream.HTTPResponse(
            200,
            {},
            b'{"results":[{"name":"1.0.0"}],"next":"https://hub.docker.com/page2"}',
            "https://hub.docker.com/page1",
        )
        second = upstream.HTTPResponse(
            200,
            {},
            b'{"results":[{"name":"1.1.0"}],"next":null}',
            "https://hub.docker.com/page2",
        )
        client = StaticClient([first, second])
        self.assertEqual(
            upstream._docker_hub_tags(client, "docker.io/library/example"),
            ["1.0.0", "1.1.0"],
        )

    def test_docker_hub_single_segment_uses_library_namespace(self) -> None:
        """Resolve an unqualified Docker Hub repository through its library namespace."""

        response = upstream.HTTPResponse(
            200,
            {},
            b'{"results":[{"name":"1.0.0"}],"next":null}',
            "https://hub.test/page1",
        )
        client = StaticClient([response])

        self.assertEqual(
            upstream._docker_hub_tags(client, "docker.io/example"),
            ["1.0.0"],
        )
        self.assertEqual(
            client.calls[0][0],
            "https://hub.docker.com/v2/repositories/library/example/tags",
        )

    def test_docker_hub_malformed_repository_is_rejected(self) -> None:
        """Reject empty repository path components before querying Docker Hub."""

        for repository in (
            "docker.io/",
            "docker.io//example",
            "docker.io/library/",
        ):
            with (
                self.subTest(repository=repository),
                self.assertRaisesRegex(
                    pins.VersioningError,
                    "Docker Hub repository is malformed",
                ),
            ):
                upstream._docker_hub_tags(mock.Mock(), repository)

    def test_oci_bearer_authentication_and_digest(self) -> None:
        """Check the token exchange and digest header with real retry delays suppressed."""

        digest = "sha256:" + ("c" * 64)
        unauthorized = FakeResponse(
            status_code=401,
            headers={
                "www-authenticate": (
                    'Bearer realm="https://auth.docker.io/token",'
                    'service="registry.docker.io",scope="repository:team/image:pull"'
                )
            },
        )

        token = FakeResponse(
            content=b'{"token":"secret"}',
            url="https://auth.docker.io/token",
        )
        manifest = FakeResponse(
            headers={"docker-content-digest": digest},
            content=b"manifest",
            url="https://registry-1.docker.io/v2/team/image/manifests/v1.0.0",
        )
        session = QueueSession([unauthorized, token, manifest])
        self.assertEqual(
            upstream._oci_digest(
                upstream.HTTPClient(session=session, sleep=lambda _: None),
                "docker.io/team/image:v1.0.0",
            ),
            digest,
        )
        self.assertEqual(len(session.calls), 3)
        self.assertEqual(
            session.calls[-1][1]["headers"]["Authorization"],
            "Bearer secret",
        )


class SourceParsingTests(unittest.TestCase):
    """Check source parsing, version policy, and command exit results offline."""

    def test_two_part_release_tag_matches_three_part_pin(self) -> None:
        """Normalize a two-part upstream release to the existing three-part pin format."""

        self.assertEqual(
            upstream._normalize_like("1.2.0", "1.3"),
            "1.3.0",
        )

    def test_go_malformed_response_becomes_error(self) -> None:
        """Report a malformed Go release document as a failed resolution."""

        response = upstream.HTTPResponse(
            200, {}, b'{"not":"a release list"}', "https://go.dev/dl/"
        )
        spec = pins.REGISTRY["GO_VERSION"]
        result = upstream.resolve_spec(
            StaticClient([response]),
            spec,
            {"GO_VERSION": "1.26.5"},
        )
        self.assertEqual(result.status, "error")

    def test_bcr_ignores_yanked_releases(self) -> None:
        """Keep the current Bazel module when the only newer release was yanked."""

        response = upstream.HTTPResponse(
            200,
            {},
            b'{"versions":["1.0.0","1.1.0"],'
            b'"yanked_versions":{"1.1.0":"regression"}}',
            "https://bcr.bazel.build/modules/example/metadata.json",
        )
        spec = pins.VersionSpec(
            key="EXAMPLE_VERSION",
            display_name="example",
            category="test",
            pattern=pins.PLAIN_SEMVER,
            source=pins.Source.BCR,
            source_id="example",
        )

        result = upstream.resolve_spec(
            StaticClient([response]),
            spec,
            {"EXAMPLE_VERSION": "1.0.0"},
        )

        self.assertEqual(result.status, "up-to-date")
        self.assertEqual(result.latest, "1.0.0")

    def test_nvidia_helm_index(self) -> None:
        """Select the newest chart version from an NVIDIA Helm index fixture."""

        response = upstream.HTTPResponse(
            200,
            {},
            b"entries:\n  gpu-operator:\n    - version: 26.3.2\n"
            b"    - version: 26.3.3\n",
            "https://helm.ngc.nvidia.com/nvidia/index.yaml",
        )
        self.assertEqual(
            upstream._nvidia_helm_version(StaticClient([response]), "gpu-operator"),
            "26.3.3",
        )

    def test_malformed_checksum_is_rejected(self) -> None:
        """Reject artifact text that does not contain a valid SHA-256 checksum."""

        response = upstream.HTTPResponse(
            200, {}, b"not-a-checksum  go.tar.gz\n", "https://artifact.test"
        )
        spec = pins.REGISTRY["GO_LINUX_AMD64_SHA256"]
        values = {
            "GO_VERSION": "1.26.5",
            "GO_LINUX_AMD64_SHA256": "a" * 64,
        }
        with self.assertRaisesRegex(pins.VersioningError, "malformed SHA256"):
            upstream._artifact_checksum(StaticClient([response]), spec, values)

    def test_checksum_list_selects_the_pinned_archive(self) -> None:
        """Pick the pinned archive's line from a release checksum list, not the first line."""

        listing = (
            f"{'a' * 64}  gitleaks_8.30.1_darwin_x64.tar.gz\n"
            f"{'b' * 64}  gitleaks_8.30.1_linux_x64.tar.gz\n"
            f"{'c' * 64}  gitleaks_8.30.1_linux_arm64.tar.gz\n"
        ).encode()
        response = upstream.HTTPResponse(200, {}, listing, "https://artifact.test")
        spec = pins.REGISTRY["GITLEAKS_LINUX_AMD64_SHA256"]
        values = {"GITLEAKS_VERSION": "8.30.1"}

        self.assertEqual(
            upstream._artifact_checksum(StaticClient([response]), spec, values),
            "b" * 64,
        )

        missing = upstream.HTTPResponse(
            200,
            {},
            f"{'a' * 64}  gitleaks_8.30.1_darwin_x64.tar.gz\n".encode(),
            "https://artifact.test",
        )
        with self.assertRaisesRegex(pins.VersioningError, "lacks unique"):
            upstream._artifact_checksum(StaticClient([missing]), spec, values)

    def test_unknown_derived_key_becomes_resolution_error(self) -> None:
        """Return an error result for an unsupported derived version key."""

        spec = pins.VersionSpec(
            key="UNKNOWN_DERIVED_VERSION",
            display_name="unknown derived version",
            category="test",
            pattern=pins.PLAIN_SEMVER,
            source=pins.Source.DERIVED,
            update_policy=pins.UpdatePolicy.DERIVED,
        )

        result = upstream.resolve_spec(
            mock.Mock(),
            spec,
            {"UNKNOWN_DERIVED_VERSION": "1.0.0"},
        )

        self.assertEqual(result.status, "error")
        self.assertIn("unsupported derived version key", result.detail)

    def test_moving_tag_digest_change_is_an_update(self) -> None:
        """Treat a new digest for an unchanged moving tag as an available update."""

        spec = pins.REGISTRY["UBI9_IMAGE_DIGEST"]
        values = {
            "UBI9_IMAGE_REPOSITORY": "registry.access.redhat.com/ubi9/ubi",
            "UBI9_IMAGE_TAG": "latest",
            "UBI9_IMAGE_DIGEST": "sha256:" + ("a" * 64),
        }
        with mock.patch.object(
            upstream,
            "_oci_digest",
            return_value="sha256:" + ("b" * 64),
        ):
            result = upstream.resolve_spec(mock.Mock(), spec, values)
        self.assertEqual(result.status, "update-available")

    def test_oci_tag_repository_is_read_from_versions_env(self) -> None:
        """Require the resolver to query the repository selected by canonical pins."""

        spec = pins.REGISTRY["BUSYBOX_IMAGE_TAG"]
        values = {
            "BUSYBOX_IMAGE_REPOSITORY": "docker.io/library/busybox",
            "BUSYBOX_IMAGE_TAG": "1.38.0",
        }
        with mock.patch.object(
            upstream,
            "_docker_hub_tags",
            return_value=["1.38.0"],
        ) as tags:
            result = upstream.resolve_spec(mock.Mock(), spec, values)
        self.assertEqual(result.status, "up-to-date")
        tags.assert_called_once_with(mock.ANY, values["BUSYBOX_IMAGE_REPOSITORY"])

    def test_strict_exit_codes(self) -> None:
        """Distinguish current pins, available updates, and incomplete checks by exit code."""

        spec = pins.REGISTRY["GO_VERSION"]
        current = upstream.Resolution(spec, "1.0.0", "1.0.0", "up-to-date")
        update = upstream.Resolution(spec, "1.0.0", "1.1.0", "update-available")
        error = upstream.Resolution(spec, "1.0.0", "", "error", "failed")
        self.assertEqual(cli._strict_exit([current], []), 0)
        self.assertEqual(cli._strict_exit([update], []), 1)
        self.assertEqual(cli._strict_exit([update, error], []), 2)
        module = upstream.ModuleUpdate(
            "example/module",
            "v1.0.0",
            "v1.1.0",
            False,
        )
        self.assertEqual(cli._strict_exit([current], [module]), 1)

    def test_failed_test_subcommand_returns_error(self) -> None:
        """Propagate a failed unittest result through the public test command result."""

        result = mock.Mock()
        result.wasSuccessful.return_value = False
        runner = mock.Mock()
        runner.run.return_value = result
        with (
            mock.patch.object(
                unittest.defaultTestLoader,
                "discover",
                return_value=mock.sentinel.suite,
            ),
            mock.patch.object(unittest, "TextTestRunner", return_value=runner),
        ):
            self.assertEqual(cli.test_versions(HACK_DIR.parent), cli.EXIT_ERROR)
        runner.run.assert_called_once_with(mock.sentinel.suite)

    def test_apply_preserves_manual_policy_entries(self) -> None:
        """Offer one automatic update while requiring every manual key to stay untouched."""

        values = pins.parse_versions_env(HACK_DIR / "versions.env")

        def resolve(_client, spec, proposed):
            """Advertise a UV update and echo every other proposed pin without network access."""

            current = proposed[spec.key]
            if spec.key == "UV_VERSION":
                return upstream.Resolution(
                    spec,
                    current,
                    "99.0.0",
                    "update-available",
                )
            return upstream.Resolution(
                spec,
                current,
                current,
                "up-to-date",
            )

        with mock.patch.object(upstream, "resolve_spec", side_effect=resolve):
            updates = cli._complete_apply_updates(mock.Mock(), values)

        self.assertEqual(updates["UV_VERSION"], "99.0.0")
        for spec in pins.SPECS:
            if spec.update_policy is pins.UpdatePolicy.MANUAL:
                with self.subTest(key=spec.key):
                    self.assertNotIn(spec.key, updates)


if __name__ == "__main__":
    unittest.main()
