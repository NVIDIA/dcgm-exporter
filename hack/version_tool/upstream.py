# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Discover dependency updates from Go and external version sources."""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import tempfile
import time
from collections.abc import Callable, Iterable, Mapping
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from functools import cache
from pathlib import Path
from threading import Lock, local
from types import MappingProxyType
from typing import Any, Literal
from urllib.parse import unquote, urljoin, urlparse

import requests
import yaml
from packaging.version import InvalidVersion, Version

from .pins import SPECS, Source, UpdatePolicy, VersionSpec, VersioningError

GO_COMMAND_TIMEOUT = 15 * 60
# Keep these modules fixed until Kubernetes and kube-openapi use the same
# structured-merge-diff major. Newer kustomize/api and kyaml raise kube-openapi
# together. Bazel also needs the checked-in patch for kyaml's embedded schemas.
GO_MODULE_UPGRADE_LOCKS = frozenset(
    {
        "k8s.io/kube-openapi",
        "sigs.k8s.io/kustomize/api",
        "sigs.k8s.io/kustomize/kyaml",
    }
)
REQUEST_TIMEOUT = 30
MAX_ATTEMPTS = 3
MAX_REDIRECTS = 10
MANIFEST_ACCEPT = (
    "application/vnd.oci.image.index.v1+json,"
    "application/vnd.docker.distribution.manifest.list.v2+json,"
    "application/vnd.oci.image.manifest.v1+json,"
    "application/vnd.docker.distribution.manifest.v2+json"
)

Status = Literal[
    "up-to-date",
    "update-available",
    "ahead",
    "manual",
    "error",
]


def _run_go(
    arguments: list[str],
    *,
    cwd: Path,
    env: Mapping[str, str],
) -> None:
    """Run a bounded Go command and normalize failures for CLI reporting."""

    try:
        result = subprocess.run(
            ["go", *arguments],
            cwd=cwd,
            env=dict(env),
            check=False,
            capture_output=True,
            text=True,
            timeout=GO_COMMAND_TIMEOUT,
        )
    except subprocess.TimeoutExpired as error:
        raise VersioningError(
            f"go {' '.join(arguments)} timed out after {GO_COMMAND_TIMEOUT}s"
        ) from error
    except OSError as error:
        raise VersioningError(
            f"cannot run go {' '.join(arguments)}: {error}"
        ) from error
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        raise VersioningError(
            f"go {' '.join(arguments)} failed: "
            f"{detail or f'exit status {result.returncode}'}"
        )

def build_go_module_candidates(
    repo_root: Path,
    base_mod: str,
    base_sum: str,
    values: Mapping[str, str],
) -> tuple[str, str]:
    """Upgrade and verify the module graph without modifying the repository."""

    with tempfile.TemporaryDirectory(prefix="dcgm-exporter-versions-") as name:
        root = Path(name)
        modfile = root / "candidate.mod"
        sumfile = root / "candidate.sum"
        modfile.write_text(base_mod)
        sumfile.write_text(base_sum)

        env = dict(os.environ)
        env["GOTOOLCHAIN"] = f"go{values['GO_VERSION']}"
        env["GOWORK"] = "off"
        env["GOFLAGS"] = f"-modfile={modfile}"
        _run_go(["get", "-u", "-t", "./..."], cwd=repo_root, env=env)
        requirements = _go_mod_requirements(base_mod)
        for module in sorted(GO_MODULE_UPGRADE_LOCKS):
            requirement = requirements.get(module)
            if requirement is not None:
                _run_go(
                    ["get", f"{module}@{requirement[0]}"],
                    cwd=repo_root,
                    env=env,
                )
        _run_go(
            ["mod", "tidy", f"-go={values['GO_VERSION']}"],
            cwd=repo_root,
            env=env,
        )

        candidate_mod = modfile.read_text()
        candidate_mod, go_count = re.subn(
            r"^go \S+$",
            f"go {values['GO_VERSION']}",
            candidate_mod,
            count=1,
            flags=re.MULTILINE,
        )
        candidate_mod = re.sub(
            r"^toolchain go\S+\n(?:\n)?",
            "",
            candidate_mod,
            flags=re.MULTILINE,
        )
        if go_count != 1:
            raise VersioningError("Go module candidate lacks exactly one go directive")
        modfile.write_text(candidate_mod)

        verify_root = root / "verify"
        verify_root.mkdir()
        shutil.copy2(modfile, verify_root / "go.mod")
        shutil.copy2(sumfile, verify_root / "go.sum")
        verify_env = dict(os.environ)
        verify_env["GOTOOLCHAIN"] = f"go{values['GO_VERSION']}"
        verify_env["GOWORK"] = "off"
        verify_env.pop("GOFLAGS", None)
        _run_go(["mod", "verify"], cwd=verify_root, env=verify_env)
        return candidate_mod, sumfile.read_text()

@dataclass(frozen=True)
class Resolution:
    """Report the freshness result for one non-module registry entry."""

    spec: VersionSpec
    current: str
    latest: str
    status: Status
    detail: str = ""


@dataclass(frozen=True)
class ModuleUpdate:
    """Describe one Go module graph change for reporting or apply."""

    path: str
    current: str
    latest: str
    indirect: bool


@dataclass(frozen=True)
class HTTPResponse:
    """Small response value that keeps HTTP helpers easy to mock."""

    status_code: int
    headers: Mapping[str, str]
    content: bytes
    url: str

    @property
    def text(self) -> str:
        """Decode the response body as UTF-8 text."""

        return self.content.decode("utf-8")

    def json(self) -> Any:
        """Decode the response body as JSON."""

        return json.loads(self.content)


def _decode_json_stream(content: str) -> list[dict[str, object]]:
    """Decode the concatenated JSON documents emitted by `go list`."""

    decoder = json.JSONDecoder()
    index = 0
    documents: list[dict[str, object]] = []
    while index < len(content):
        while index < len(content) and content[index].isspace():
            index += 1
        if index >= len(content):
            break
        document, index = decoder.raw_decode(content, index)
        if isinstance(document, dict):
            documents.append(document)
    return documents


def _module_updates(documents: list[dict[str, object]]) -> list[ModuleUpdate]:
    """Extract available module updates from `go list -m -u` records."""

    updates: list[ModuleUpdate] = []
    for module in documents:
        update = module.get("Update")
        if not isinstance(update, dict):
            continue
        path = module.get("Path")
        current = module.get("Version")
        latest = update.get("Version")
        if not all(isinstance(value, str) for value in (path, current, latest)):
            continue
        updates.append(
            ModuleUpdate(
                path=path,
                current=current,
                latest=latest,
                indirect=bool(module.get("Indirect", False)),
            )
        )
    return sorted(updates, key=lambda item: (item.indirect, item.path))


def discover_module_updates(repo_root: Path) -> list[ModuleUpdate]:
    """Query the current Go graph for direct and transitive updates."""

    env = dict(os.environ)
    env["GOTOOLCHAIN"] = "auto"
    env["GOWORK"] = "off"
    try:
        result = subprocess.run(
            ["go", "list", "-m", "-u", "-json", "all"],
            cwd=repo_root,
            env=env,
            check=False,
            capture_output=True,
            text=True,
            timeout=GO_COMMAND_TIMEOUT,
        )
    except subprocess.TimeoutExpired as error:
        raise VersioningError(
            f"go list timed out after {GO_COMMAND_TIMEOUT}s"
        ) from error
    except OSError as error:
        raise VersioningError(f"cannot run go list: {error}") from error
    if result.returncode != 0:
        detail = result.stderr.strip() or f"exit status {result.returncode}"
        raise VersioningError(f"go list failed: {detail}")
    try:
        return _module_updates(_decode_json_stream(result.stdout))
    except json.JSONDecodeError as error:
        raise VersioningError(f"go list returned malformed JSON: {error}") from error


def _go_mod_requirements(content: str) -> dict[str, tuple[str, bool]]:
    """Parse required module versions and indirect markers from go.mod text."""

    requirements: dict[str, tuple[str, bool]] = {}
    for line in content.splitlines():
        match = re.fullmatch(
            r"\s*([^\s()]+)\s+(v\S+)(\s+// indirect)?\s*",
            line,
        )
        if match:
            requirements[match.group(1)] = (
                match.group(2),
                match.group(3) is not None,
            )
    return requirements


def _compatible_module_updates(
    current_mod: str,
    candidate_mod: str,
) -> list[ModuleUpdate]:
    """Compare current and candidate compatible Go module requirements."""

    current = _go_mod_requirements(current_mod)
    candidate = _go_mod_requirements(candidate_mod)
    updates: list[ModuleUpdate] = []
    for path in sorted(set(current) | set(candidate)):
        current_version, current_indirect = current.get(
            path,
            ("(not required)", True),
        )
        latest, latest_indirect = candidate.get(
            path,
            ("(removed)", current_indirect),
        )
        if (current_version, current_indirect) == (latest, latest_indirect):
            continue
        updates.append(
            ModuleUpdate(
                path=path,
                current=current_version,
                latest=latest,
                indirect=latest_indirect,
            )
        )
    return sorted(updates, key=lambda item: (item.indirect, item.path))


def discover_compatible_module_updates(
    repo_root: Path,
    values: Mapping[str, str],
) -> list[ModuleUpdate]:
    """Build a candidate module graph and report its compatible changes."""

    current_mod = (repo_root / "go.mod").read_text()
    candidate_mod, _ = build_go_module_candidates(
        repo_root,
        current_mod,
        (repo_root / "go.sum").read_text(),
        values,
    )
    return _compatible_module_updates(current_mod, candidate_mod)


class HTTPClient:
    """HTTP GET client with bounded retries and thread-owned sessions.

    Normal use creates one Requests session per worker thread. Tests may inject
    one shared session; access to that override is serialized.
    """

    def __init__(
        self,
        *,
        session: requests.Session | None = None,
        sleep: Any = time.sleep,
    ) -> None:
        """Initialize optional test overrides and per-thread session storage."""

        self._shared_session = session
        self._shared_session_lock = Lock()
        self._thread_state = local()
        self.sleep = sleep

    def _session_for_thread(self) -> requests.Session:
        """Return the injected session or lazily create this thread's session."""

        if self._shared_session is not None:
            return self._shared_session
        session = getattr(self._thread_state, "session", None)
        if session is None:
            session = requests.Session()
            self._thread_state.session = session
        return session

    def _get_once(
        self,
        url: str,
        *,
        headers: Mapping[str, str],
        params: Mapping[str, str | int] | None,
        allow_redirects: bool = True,
    ) -> requests.Response:
        """Issue one GET, locking only when a test injects a shared session."""

        session = self._session_for_thread()
        arguments = {
            "headers": headers,
            "params": params,
            "timeout": REQUEST_TIMEOUT,
            "allow_redirects": allow_redirects,
        }
        if self._shared_session is None:
            return session.get(url, **arguments)
        with self._shared_session_lock:
            return session.get(url, **arguments)

    def _get_with_trusted_redirects(
        self,
        url: str,
        *,
        headers: Mapping[str, str],
        params: Mapping[str, str | int] | None,
        allowed_origins: frozenset[tuple[str, int]],
    ) -> requests.Response:
        """Follow bounded OCI redirects only after validating each destination."""

        for _ in range(MAX_REDIRECTS + 1):
            _require_oci_origin(url, allowed_origins)
            response = self._get_once(
                url,
                headers=headers,
                params=params,
                allow_redirects=False,
            )
            location = _header_value(response.headers, "location")
            if response.status_code not in {301, 302, 303, 307, 308} or not location:
                return response

            url = urljoin(response.url, location)
            params = None
            response.close()

        raise VersioningError("OCI request exceeded the redirect limit")

    def get(
        self,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        params: Mapping[str, str | int] | None = None,
        allowed_statuses: frozenset[int] = frozenset(),
        allowed_origins: frozenset[tuple[str, int]] | None = None,
    ) -> HTTPResponse:
        """GET a URL, retry transient failures, and wrap the response."""

        request_headers = dict(headers or {})

        response: requests.Response | None = None
        for attempt in range(1, MAX_ATTEMPTS + 1):
            try:
                if allowed_origins is None:
                    response = self._get_once(
                        url,
                        headers=request_headers,
                        params=params,
                    )
                else:
                    response = self._get_with_trusted_redirects(
                        url,
                        headers=request_headers,
                        params=params,
                        allowed_origins=allowed_origins,
                    )
            except requests.RequestException as error:
                if attempt == MAX_ATTEMPTS:
                    raise VersioningError(
                        f"GET {url}: request failed after {MAX_ATTEMPTS} attempts: "
                        f"{error}"
                    ) from error
                self.sleep(_retry_delay({}, attempt))
                continue
            if response.status_code not in {429, 500, 502, 503, 504}:
                break
            if attempt == MAX_ATTEMPTS:
                break
            delay = _retry_delay(response.headers, attempt)
            self.sleep(delay)

        assert response is not None
        wrapped = HTTPResponse(
            status_code=response.status_code,
            headers=dict(response.headers),
            content=response.content,
            url=response.url,
        )
        if wrapped.status_code >= 400 and wrapped.status_code not in allowed_statuses:
            raise VersioningError(f"GET {wrapped.url}: HTTP {wrapped.status_code}")
        return wrapped


def _header_value(headers: Mapping[str, str], name: str) -> str | None:
    """Look up an HTTP header without assuming key casing."""

    lowered = name.lower()
    return next(
        (value for key, value in headers.items() if key.lower() == lowered),
        None,
    )


def _retry_delay(headers: Mapping[str, str], attempt: int) -> float:
    """Honor server retry headers, then fall back to bounded backoff."""

    retry_after = _header_value(headers, "retry-after")
    if retry_after:
        try:
            return min(float(retry_after), 60.0)
        except ValueError:
            pass
    reset = _header_value(headers, "x-ratelimit-reset")
    if reset:
        try:
            return max(0.0, min(float(reset) - time.time(), 60.0))
        except ValueError:
            pass
    return float(min(2 ** (attempt - 1), 10))


def _github_headers() -> dict[str, str]:
    """Build GitHub API headers, adding authentication when configured."""

    headers = {
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    if token := os.environ.get("GITHUB_TOKEN"):
        headers["Authorization"] = f"Bearer {token}"
    return headers


def _normalize_like(current: str, latest: str) -> str:
    """Match upstream spelling to the current pin's version style."""

    if "k3s" in current:
        latest = latest.replace("+k3s", "-k3s")

    # Some upstream release tags omit ``.0``. Preserve a current three-part
    # semantic-version style so the generated value stays valid in versions.env
    # and in Bzlmod declarations.
    current_numeric = current.lstrip("v")
    latest_numeric = latest.lstrip("v")
    if re.fullmatch(r"\d+\.\d+\.\d+", current_numeric) and re.fullmatch(
        r"\d+\.\d+", latest_numeric
    ):
        latest = f"{latest}.0"

    if current.startswith("v") and not latest.startswith("v"):
        return f"v{latest}"
    if not current.startswith("v") and latest.startswith("v"):
        return latest[1:]
    return latest


def _parsed_version(value: str) -> Version | None:
    """Parse supported tag styles into comparable packaging versions."""

    normalized = value.lstrip("v").replace("-k3s", "+k3s")
    if normalized.endswith("-base"):
        normalized = normalized.removesuffix("-base")
    try:
        return Version(normalized)
    except InvalidVersion:
        match = re.match(r"(\d+(?:\.\d+)+)", normalized)
        if not match:
            return None
        try:
            return Version(match.group(1))
        except InvalidVersion:
            return None


def _status(current: str, latest: str) -> Status:
    """Classify the relationship between a current and discovered value."""

    if current == latest:
        return "up-to-date"
    current_version = _parsed_version(current)
    latest_version = _parsed_version(latest)
    if current_version is None or latest_version is None:
        return "update-available"
    if current_version < latest_version:
        return "update-available"
    if current_version > latest_version:
        return "ahead"
    return "update-available"


def _latest_version(values: list[str]) -> str:
    """Select the newest stable semantic version from upstream candidates."""

    stable: list[tuple[Version, str]] = []
    for value in values:
        lowered = value.lower()
        if any(marker in lowered for marker in ("alpha", "beta", "rc", "preview")):
            continue
        parsed = _parsed_version(value)
        if parsed is not None and not parsed.is_prerelease:
            stable.append((parsed, value))
    if not stable:
        raise VersioningError("upstream returned no stable semantic versions")
    # Repositories can publish aliases such as v7 and v7.0.0 for the same
    # semantic version. Prefer the more explicit tag so fallback results match
    # release metadata and do not report false drift.
    return max(
        stable,
        key=lambda item: (item[0], len(item[1]), item[1]),
    )[1]


def _github_api_limited(error: VersioningError) -> bool:
    """Identify unauthenticated API limits eligible for Git-ref fallback."""

    return not os.environ.get("GITHUB_TOKEN") and any(
        status in str(error) for status in ("HTTP 403", "HTTP 429")
    )


@cache
def _git_remote_tags(repo: str) -> dict[str, str]:
    """Return GitHub tag-to-commit mappings without using the REST API."""
    try:
        completed = subprocess.run(
            [
                "git",
                "ls-remote",
                "--tags",
                f"https://github.com/{repo}.git",
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=REQUEST_TIMEOUT,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise VersioningError(f"cannot query Git refs for {repo}: {error}") from error
    if completed.returncode != 0:
        detail = completed.stderr.strip() or f"exit {completed.returncode}"
        raise VersioningError(f"cannot query Git refs for {repo}: {detail}")

    tags: dict[str, str] = {}
    peeled: set[str] = set()
    prefix = "refs/tags/"
    for line in completed.stdout.splitlines():
        sha, separator, ref = line.partition("\t")
        if (
            not separator
            or not re.fullmatch(r"[0-9a-f]{40}", sha)
            or not ref.startswith(prefix)
        ):
            continue
        tag = ref.removeprefix(prefix)
        if tag.endswith("^{}"):
            tag = tag.removesuffix("^{}")
            tags[tag] = sha
            peeled.add(tag)
        elif tag not in peeled:
            tags[tag] = sha
    if not tags:
        raise VersioningError(f"Git refs for {repo} contain no tags")
    return tags


def _github_latest_release(client: HTTPClient, repo: str) -> str:
    """Resolve GitHub's latest stable release, including during API limits."""

    try:
        response = client.get(
            f"https://api.github.com/repos/{repo}/releases/latest",
            headers=_github_headers(),
        )
    except VersioningError as error:
        if not _github_api_limited(error):
            raise
        response = client.get(f"https://github.com/{repo}/releases/latest")
        parsed_url = urlparse(response.url)
        tag_path = f"/{repo}/releases/tag/"
        if (
            parsed_url.scheme != "https"
            or parsed_url.netloc.lower() != "github.com"
            or not parsed_url.path.startswith(tag_path)
        ):
            raise VersioningError(
                f"GitHub latest release for {repo} did not redirect to a tag"
            )
        tag = unquote(parsed_url.path.removeprefix(tag_path))
        if not tag:
            raise VersioningError(f"GitHub latest release for {repo} lacks tag_name")
        return tag
    tag = response.json().get("tag_name")
    if not isinstance(tag, str) or not tag:
        raise VersioningError(f"GitHub latest release for {repo} lacks tag_name")
    return tag


def _github_latest_tag(client: HTTPClient, source_id: str) -> str:
    """Resolve the newest matching tag across paginated GitHub results."""

    repo, _, prefix = source_id.partition(":")
    prefix = prefix or "v"
    names: list[str] = []
    try:
        for page in range(1, 11):
            response = client.get(
                f"https://api.github.com/repos/{repo}/tags",
                headers=_github_headers(),
                params={"per_page": 100, "page": page},
            )
            items = response.json()
            if not isinstance(items, list):
                raise VersioningError(f"GitHub tags for {repo} are malformed")
            names.extend(
                item["name"]
                for item in items
                if isinstance(item, dict)
                and isinstance(item.get("name"), str)
                and item["name"].startswith(prefix)
            )
            if len(items) < 100:
                break
    except VersioningError as error:
        if not _github_api_limited(error):
            raise
        names = [tag for tag in _git_remote_tags(repo) if tag.startswith(prefix)]
    return _latest_version(names)


def _github_tag_sha(client: HTTPClient, repo: str, tag: str) -> str:
    """Resolve a lightweight or annotated GitHub tag to its commit SHA."""

    try:
        response = client.get(
            f"https://api.github.com/repos/{repo}/git/ref/tags/{tag}",
            headers=_github_headers(),
        )
    except VersioningError as error:
        if not _github_api_limited(error):
            raise
        sha = _git_remote_tags(repo).get(tag)
        if sha is None:
            raise VersioningError(f"Git ref {repo}@{tag} does not exist")
        return sha
    obj = response.json().get("object", {})
    sha = obj.get("sha")
    obj_type = obj.get("type")
    if not isinstance(sha, str):
        raise VersioningError(f"GitHub ref {repo}@{tag} lacks a SHA")
    if obj_type == "tag":
        tag_response = client.get(
            f"https://api.github.com/repos/{repo}/git/tags/{sha}",
            headers=_github_headers(),
        )
        sha = tag_response.json().get("object", {}).get("sha")
    if not isinstance(sha, str) or not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise VersioningError(f"GitHub ref {repo}@{tag} resolved malformed SHA")
    return sha


def _nvidia_helm_version(client: HTTPClient, chart: str) -> str:
    """Resolve the newest stable chart version in NVIDIA's Helm index."""

    response = client.get("https://helm.ngc.nvidia.com/nvidia/index.yaml")
    document = yaml.safe_load(response.content)
    entries = document.get("entries", {}) if isinstance(document, dict) else {}
    versions = entries.get(chart)
    if not isinstance(versions, list):
        raise VersioningError(f"NVIDIA Helm index lacks chart {chart}")
    return _latest_version(
        [
            item["version"]
            for item in versions
            if isinstance(item, dict) and isinstance(item.get("version"), str)
        ]
    )


def _docker_hub_tags(client: HTTPClient, repository: str) -> list[str]:
    """Collect Docker Hub tags across API result pages."""

    parsed = repository.removeprefix("docker.io/")
    namespace, separator, name = parsed.partition("/")
    if not separator:
        namespace, name = "library", namespace
    if not namespace or not name:
        raise VersioningError(f"Docker Hub repository is malformed: {repository}")
    names: list[str] = []
    url = f"https://hub.docker.com/v2/repositories/{namespace}/{name}/tags"
    params: Mapping[str, str | int] | None = {
        "page_size": 100,
        "ordering": "last_updated",
    }
    for _ in range(10):
        response = client.get(
            url,
            params=params,
            allowed_origins=frozenset({("hub.docker.com", 443)}),
        )
        payload = response.json()
        results = payload.get("results") if isinstance(payload, dict) else None
        if not isinstance(results, list):
            raise VersioningError(f"Docker Hub tags for {repository} are malformed")
        names.extend(
            item["name"]
            for item in results
            if isinstance(item, dict) and isinstance(item.get("name"), str)
        )
        next_url = payload.get("next")
        if not isinstance(next_url, str) or not next_url:
            break
        url = next_url
        params = None
    return names


def _parse_image(reference: str) -> tuple[str, str, str]:
    """Split a fully qualified OCI reference into registry, path, and tag."""

    without_scheme = reference.removeprefix("https://").removeprefix("http://")
    registry, separator, remainder = without_scheme.partition("/")
    if not separator:
        raise VersioningError(f"OCI reference lacks registry: {reference}")
    repository, separator, tag = remainder.rpartition(":")
    if not separator or not repository or not tag:
        raise VersioningError(f"OCI reference lacks tag: {reference}")
    return registry, repository, tag


def _bearer_parameters(header: str) -> dict[str, str]:
    """Parse parameters from an OCI Bearer authentication challenge."""

    if not header.lower().startswith("bearer "):
        return {}
    result: dict[str, str] = {}
    for match in re.finditer(r'(\w+)="([^"]*)"', header[7:]):
        result[match.group(1)] = match.group(2)
    return result


def _https_origin(url: str) -> tuple[str, int]:
    """Return an HTTPS origin, rejecting ambiguous URLs and embedded credentials."""

    if any(ord(character) <= 32 or ord(character) == 127 for character in url):
        raise VersioningError("OCI URL contains whitespace or control characters")
    try:
        parsed = urlparse(url)
        port = parsed.port if parsed.port is not None else 443
    except ValueError as error:
        raise VersioningError("OCI URL has an invalid host or port") from error
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.fragment
        or port == 0
    ):
        raise VersioningError(
            "OCI URL must use HTTPS without credentials or a fragment"
        )
    return parsed.hostname, port


def _require_oci_origin(
    url: str,
    allowed_origins: frozenset[tuple[str, int]],
) -> None:
    """Reject a response-controlled OCI destination outside its trusted origins."""

    if _https_origin(url) not in allowed_origins:
        raise VersioningError("OCI URL points outside the trusted origins")


def _registry_get(
    client: HTTPClient,
    url: str,
    *,
    registry: str,
    headers: Mapping[str, str] | None = None,
    params: Mapping[str, str | int] | None = None,
) -> HTTPResponse:
    """GET from an OCI registry, performing one bearer-token exchange."""

    registry_host = "registry-1.docker.io" if registry == "docker.io" else registry
    registry_origin = _https_origin(f"https://{registry_host}")
    registry_origins = frozenset({registry_origin})
    _require_oci_origin(url, registry_origins)
    response = client.get(
        url,
        headers=headers,
        params=params,
        allowed_statuses=frozenset({401}),
        allowed_origins=registry_origins,
    )
    if response.status_code != 401:
        return response
    auth = _bearer_parameters(_header_value(response.headers, "www-authenticate") or "")
    realm = auth.pop("realm", "")
    if not realm:
        raise VersioningError(f"OCI registry {registry} omitted bearer realm")
    auth_origins = registry_origins
    if registry_origin == ("registry-1.docker.io", 443):
        auth_origins = auth_origins | {("auth.docker.io", 443)}
    _require_oci_origin(realm, auth_origins)
    token_response = client.get(
        realm,
        params={key: value for key, value in auth.items() if value},
        allowed_origins=auth_origins,
    )
    token_payload = token_response.json()
    token = token_payload.get("token") or token_payload.get("access_token")
    if not isinstance(token, str):
        raise VersioningError(f"OCI token response for {registry} is malformed")
    authorized = dict(headers or {})
    authorized["Authorization"] = f"Bearer {token}"
    return client.get(
        url,
        headers=authorized,
        params=params,
        allowed_origins=registry_origins,
    )


def _oci_tags(client: HTTPClient, repository: str) -> list[str]:
    """Collect OCI tags while following registry pagination links."""

    reference = repository + ":tags"
    registry, image, _ = _parse_image(reference)
    registry_host = "registry-1.docker.io" if registry == "docker.io" else registry
    url = f"https://{registry_host}/v2/{image}/tags/list"
    params: Mapping[str, str | int] | None = {"n": 100}
    tags: list[str] = []
    for _ in range(20):
        response = _registry_get(
            client,
            url,
            registry=registry,
            params=params,
        )
        payload = response.json()
        page_tags = payload.get("tags") if isinstance(payload, dict) else None
        if not isinstance(page_tags, list):
            raise VersioningError(f"OCI tag list for {repository} is malformed")
        tags.extend(tag for tag in page_tags if isinstance(tag, str))
        link = _header_value(response.headers, "link")
        if not link:
            break
        match = re.match(r"<([^>]+)>;\s*rel=\"?next\"?", link)
        if not match:
            raise VersioningError(f"OCI tag pagination link is malformed: {link}")
        url = urljoin(url, match.group(1))
        params = None
    return tags


def _oci_digest(client: HTTPClient, reference: str) -> str:
    """Resolve an OCI manifest digest, hashing content only as fallback."""

    registry, repository, tag = _parse_image(reference)
    registry_host = "registry-1.docker.io" if registry == "docker.io" else registry
    url = f"https://{registry_host}/v2/{repository}/manifests/{tag}"
    headers = {"Accept": MANIFEST_ACCEPT}
    response = _registry_get(
        client,
        url,
        registry=registry,
        headers=headers,
    )
    digest = _header_value(response.headers, "docker-content-digest")
    if not digest:
        import hashlib

        digest = f"sha256:{hashlib.sha256(response.content).hexdigest()}"
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise VersioningError(f"OCI registry returned malformed digest for {reference}")
    return digest


def _checksum_list_filename(spec: VersionSpec, values: Mapping[str, str]) -> str:
    """Name the file whose line holds this checksum in a release checksum list.

    An empty name means the endpoint serves only this file's checksum.
    """

    if spec.key.startswith("K3D_"):
        arch = "amd64" if "AMD64" in spec.key else "arm64"
        return f"k3d-linux-{arch}"
    if spec.key == "GITLEAKS_LINUX_AMD64_SHA256":
        return f"gitleaks_{values['GITLEAKS_VERSION']}_linux_x64.tar.gz"
    return ""


def _artifact_checksum(
    client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
) -> str:
    """Read a SHA-256 companion value from an official artifact endpoint."""

    url = spec.source_id.format_map(values)
    response = client.get(url)
    lines = response.text.strip().splitlines()
    if not lines:
        raise VersioningError(f"checksum endpoint returned empty response: {url}")

    filename = _checksum_list_filename(spec, values)
    if filename:
        matches = [
            line.split()[0]
            for line in lines
            if len(line.split()) >= 2
            and Path(line.split()[-1].lstrip("*")).name == filename
        ]
        if len(matches) != 1:
            raise VersioningError(f"checksum list {url} lacks unique {filename}")
        checksum = matches[0]
    else:
        checksum = lines[0].split()[0]
    if not re.fullmatch(r"[0-9a-f]{64}", checksum):
        raise VersioningError(f"checksum endpoint returned malformed SHA256: {url}")
    return checksum


def _format_source_id(spec: VersionSpec, values: Mapping[str, str]) -> str:
    """Substitute canonical values into an upstream source template."""

    try:
        return spec.source_id.format_map(values)
    except KeyError as error:
        raise VersioningError(
            f"{spec.key} source references missing key {error.args[0]}"
        ) from error


def _resolve_derived(
    _client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
    _current: str,
) -> str:
    """Compute a value derived from another canonical registry entry."""

    if spec.key == "KUBECTL_VERSION":
        return values["K3S_VERSION"].split("-", 1)[0]
    raise VersioningError(f"unsupported derived version key: {spec.key}")


def _resolve_go_dev(
    client: HTTPClient,
    _spec: VersionSpec,
    _values: Mapping[str, str],
    _current: str,
) -> str:
    """Find the newest stable Go toolchain from go.dev."""

    releases = client.get("https://go.dev/dl/?mode=json").json()
    stable = [
        item["version"].removeprefix("go")
        for item in releases
        if isinstance(item, dict)
        and item.get("stable") is True
        and isinstance(item.get("version"), str)
    ]
    return _latest_version(stable)


def _resolve_bcr(
    client: HTTPClient,
    spec: VersionSpec,
    _values: Mapping[str, str],
    _current: str,
) -> str:
    """Find the latest non-yanked version in the Bazel Central Registry."""

    document = client.get(
        f"https://bcr.bazel.build/modules/{spec.source_id}/metadata.json"
    ).json()
    versions = document.get("versions")
    yanked = document.get("yanked_versions", {})
    if not isinstance(versions, list) or not all(
        isinstance(version, str) for version in versions
    ):
        raise VersioningError(
            f"Bazel Central Registry metadata for {spec.source_id} lacks versions"
        )
    if not isinstance(yanked, dict) or not all(
        isinstance(version, str) for version in yanked
    ):
        raise VersioningError(
            f"Bazel Central Registry metadata for {spec.source_id} has malformed yanked_versions"
        )
    return _latest_version([version for version in versions if version not in yanked])


def _resolve_github_release(
    client: HTTPClient,
    spec: VersionSpec,
    _values: Mapping[str, str],
    _current: str,
) -> str:
    """Find the newest stable release published by a GitHub repository."""

    return _github_latest_release(client, spec.source_id)


def _resolve_github_tag(
    client: HTTPClient,
    spec: VersionSpec,
    _values: Mapping[str, str],
    _current: str,
) -> str:
    """Find the newest stable GitHub tag matching the registry prefix."""

    return _github_latest_tag(client, spec.source_id)


def _resolve_github_ref(
    client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
    _current: str,
) -> str:
    """Resolve either an Action release or its immutable companion SHA."""

    if spec.update_policy is not UpdatePolicy.COMPANION:
        return _github_latest_release(client, spec.source_id)
    rendered = _format_source_id(spec, values)
    repo, _, tag = rendered.partition(":")
    return _github_tag_sha(client, repo, tag)


def _resolve_nvidia_helm(
    client: HTTPClient,
    spec: VersionSpec,
    _values: Mapping[str, str],
    _current: str,
) -> str:
    """Find the newest chart version in NVIDIA's Helm index."""

    return _nvidia_helm_version(client, spec.source_id)


def _matching_oci_tags(current: str, names: Iterable[str]) -> list[str]:
    """Keep OCI tags with the same prefix and numeric width as the current pin."""

    numeric_current = current.lstrip("v")
    if not all(part.isdigit() for part in numeric_current.split(".")):
        return list(names)
    width = len(numeric_current.split("."))
    prefix = "v" if current.startswith("v") else ""
    pattern = re.escape(prefix) + r"\d+" + (r"\.\d+" * (width - 1))
    return [name for name in names if re.fullmatch(pattern, name)]


def _resolve_oci_tag(
    client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
    current: str,
) -> str:
    """Find the newest semantic tag without crossing tag-shape policies."""

    if _parsed_version(current) is None:
        return current
    repository = _format_source_id(spec, values)
    if repository.startswith("docker.io/"):
        names = _docker_hub_tags(client, repository)
    else:
        names = _oci_tags(client, repository)
    return _latest_version(_matching_oci_tags(current, names))


def _resolve_oci_digest(
    client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
    _current: str,
) -> str:
    """Resolve the multi-platform manifest digest for an image tag."""

    return _oci_digest(client, _format_source_id(spec, values))


def _resolve_artifact(
    client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
    _current: str,
) -> str:
    """Resolve the checksum published beside an official artifact."""

    return _artifact_checksum(client, spec, values)


# Source dispatch keeps resolve_spec flat and makes each network policy testable.
Resolver = Callable[[HTTPClient, VersionSpec, Mapping[str, str], str], str]
SOURCE_RESOLVERS: Mapping[Source, Resolver] = MappingProxyType(
    {
        Source.DERIVED: _resolve_derived,
        Source.GO_DEV: _resolve_go_dev,
        Source.BCR: _resolve_bcr,
        Source.GITHUB_RELEASE: _resolve_github_release,
        Source.GITHUB_TAG: _resolve_github_tag,
        Source.GITHUB_REF: _resolve_github_ref,
        Source.NVIDIA_HELM: _resolve_nvidia_helm,
        Source.OCI_TAG: _resolve_oci_tag,
        Source.OCI_DIGEST: _resolve_oci_digest,
        Source.ARTIFACT: _resolve_artifact,
    }
)


def resolve_spec(
    client: HTTPClient,
    spec: VersionSpec,
    values: Mapping[str, str],
) -> Resolution:
    """Resolve one registry entry and convert failures into reportable results."""

    current = values[spec.key]
    if spec.update_policy is UpdatePolicy.MANUAL:
        return Resolution(spec, current, current, "manual", spec.rationale)

    try:
        resolver = SOURCE_RESOLVERS[spec.source]
        latest = resolver(client, spec, values, current)
        latest = _normalize_like(current, latest)
        return Resolution(spec, current, latest, _status(current, latest))
    # A failed upstream belongs in the complete report instead of aborting it.
    except Exception as error:  # noqa: BLE001
        return Resolution(spec, current, "", "error", str(error))


def resolve_all(
    client: HTTPClient,
    values: Mapping[str, str],
) -> list[Resolution]:
    """Resolve registry entries concurrently and restore registry order."""

    specs = list(SPECS)
    results: list[Resolution] = []
    with ThreadPoolExecutor(max_workers=min(12, max(1, len(specs)))) as executor:
        futures = {
            executor.submit(resolve_spec, client, spec, values): spec for spec in specs
        }
        for future in as_completed(futures):
            results.append(future.result())
    order = {spec.key: index for index, spec in enumerate(SPECS)}
    return sorted(results, key=lambda item: order[item.spec.key])
