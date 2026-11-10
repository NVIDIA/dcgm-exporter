from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

HACK_DIR = Path(__file__).resolve().parents[1]
REPO_ROOT = HACK_DIR.parent
sys.path.insert(0, str(HACK_DIR))
sys.dont_write_bytecode = True

from version_tool import cli, pins, rendering, upstream, writes


def replace_env_entry(content: str, key: str, value: str | None) -> str:
    """Replace or remove exactly one canonical pin while preserving unrelated lines."""

    lines = content.splitlines(keepends=True)
    matching = [index for index, line in enumerate(lines) if line.startswith(f"{key}=")]
    if len(matching) != 1:
        raise AssertionError(f"expected exactly one {key} entry, found {len(matching)}")
    index = matching[0]
    if value is None:
        del lines[index]
    else:
        newline = "\n" if lines[index].endswith("\n") else ""
        lines[index] = f"{key}={value}{newline}"
    return "".join(lines)


class EntrypointTests(unittest.TestCase):
    """Cover behavior specific to the executable script wrapper."""

    def test_wrapper_uses_its_repository_root_from_another_directory(self) -> None:
        """Keep the executable independent of the caller's working directory."""

        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(
                [sys.executable, str(HACK_DIR / "versions.py"), "sync", "--check"],
                cwd=directory,
                capture_output=True,
                text=True,
                check=False,
                timeout=60,
            )

        self.assertEqual(result.returncode, cli.EXIT_OK, result.stderr)


class RegistryTests(unittest.TestCase):
    """Check that canonical pins match the registry and its cross-pin rules."""

    def setUp(self) -> None:
        """Read the canonical pin fixture afresh for each registry case."""

        self.content = (HACK_DIR / "versions.env").read_text()

    def test_registry_exactly_covers_versions_env(self) -> None:
        """Require registry keys and their order to match the canonical pin document."""

        values = pins.parse_versions_env_text(self.content)
        self.assertEqual(set(values), set(pins.REGISTRY))
        self.assertEqual(len(values), len(pins.SPECS))
        self.assertEqual(list(values), [spec.key for spec in pins.SPECS])

    def test_duplicate_key_is_rejected(self) -> None:
        """Reject a second occurrence of a canonical pin."""

        content = self.content + "\nGO_VERSION=1.26.5\n"
        with self.assertRaisesRegex(pins.VersioningError, "duplicate key"):
            pins.parse_versions_env_text(content)

    def test_missing_key_is_rejected(self) -> None:
        """Reject a document that omits a required registry key."""

        content = replace_env_entry(self.content, "GPU_OPERATOR_VERSION", None)
        with self.assertRaisesRegex(pins.VersioningError, "missing required"):
            pins.parse_versions_env_text(content)

    def test_unknown_key_is_rejected(self) -> None:
        """Reject a pin that has no registry policy."""

        content = self.content + "\nSURPRISE_VERSION=1.0.0\n"
        with self.assertRaisesRegex(pins.VersioningError, "unknown key"):
            pins.parse_versions_env_text(content)

    def test_empty_key_is_rejected(self) -> None:
        """Reject a required key with an empty value."""

        content = replace_env_entry(self.content, "UV_VERSION", "")
        with self.assertRaisesRegex(pins.VersioningError, "empty value"):
            pins.parse_versions_env_text(content)

    def test_each_format_rejects_an_invalid_value(self) -> None:
        """Require every registry entry to reject a value outside its schema."""

        for spec in pins.SPECS:
            with (
                self.subTest(key=spec.key),
                self.assertRaises(pins.VersioningError),
            ):
                spec.validate("not a valid pin!")

    def test_every_manual_policy_has_a_rationale(self) -> None:
        """Require a stated reason for each pin excluded from automatic updates."""

        manual = [
            spec
            for spec in pins.SPECS
            if spec.update_policy is pins.UpdatePolicy.MANUAL
        ]
        self.assertTrue(manual)
        self.assertTrue(all(spec.rationale for spec in manual))

    def test_every_automatic_source_has_a_resolver(self) -> None:
        """Require resolver coverage for every nonmanual source in the registry."""

        maintained_sources = {
            spec.source
            for spec in pins.SPECS
            if spec.update_policy is not pins.UpdatePolicy.MANUAL
        }
        self.assertEqual(maintained_sources, set(upstream.SOURCE_RESOLVERS))

    def test_buildkit_native_architecture_pin_is_explicitly_manual(self) -> None:
        """Keep the BuildKit image subject to its amd64 and arm64 support policy."""

        spec = pins.REGISTRY["BUILDKIT_IMAGE_TAG"]
        self.assertIs(spec.source, pins.Source.MANUAL)
        self.assertIs(
            spec.update_policy,
            pins.UpdatePolicy.MANUAL,
        )
        self.assertIn("amd64/arm64", spec.rationale)

    def test_image_repositories_are_canonical_registry_entries(self) -> None:
        """Require image locations to be validated, manually maintained canonical pins."""

        values = pins.parse_versions_env_text(self.content)
        repositories = [
            spec for spec in pins.SPECS if spec.category == "image-repositories"
        ]
        self.assertTrue(repositories)
        self.assertTrue(
            all(spec.key.endswith("_IMAGE_REPOSITORY") for spec in repositories)
        )
        self.assertTrue(all(spec.key in values for spec in repositories))
        self.assertTrue(
            all(spec.update_policy is pins.UpdatePolicy.MANUAL for spec in repositories)
        )

    def test_kubectl_must_align_with_k3s(self) -> None:
        """Reject kubectl when it differs from the Kubernetes release embedded in K3s."""

        values = pins.parse_versions_env_text(self.content)
        values["KUBECTL_VERSION"] = "v1.35.0"
        with self.assertRaisesRegex(pins.VersioningError, "KUBECTL_VERSION"):
            pins.validate_relationships(values)

    def test_dcgm_distribution_must_match_distroless_helper(self) -> None:
        """Reject a DCGM package suffix built for the wrong helper distribution."""

        values = pins.parse_versions_env_text(self.content)
        values["DCGM_IMAGE_TAG_SUFFIX"] = "1-ubuntu22.04"
        with self.assertRaisesRegex(
            pins.VersioningError,
            "DCGM_IMAGE_TAG_SUFFIX",
        ):
            pins.validate_relationships(values)

    def test_dcgm_image_suffix_allows_an_optional_package_revision(self) -> None:
        """Accept the supported distribution suffix with or without a package revision."""

        spec = pins.REGISTRY["DCGM_IMAGE_TAG_SUFFIX"]
        spec.validate("1-ubuntu24.04")
        spec.validate("ubuntu24.04")

    def test_builder_ubuntu_must_match_package_target(self) -> None:
        """Reject a builder release that differs from the package target distribution."""

        values = pins.parse_versions_env_text(self.content)
        values["BUILDER_UBUNTU_TAG"] = "24.04"
        with self.assertRaisesRegex(
            pins.VersioningError,
            "BUILDER_UBUNTU_TAG",
        ):
            pins.validate_relationships(values)


class WritePlanTests(unittest.TestCase):
    """Check preflight rejection and restoration of partially applied file updates."""

    def test_anchor_failure_modifies_nothing(self) -> None:
        """Reject a missing replacement anchor before changing its destination."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "target.txt"
            target.write_text("before\n")
            update = writes.AnchoredUpdate(
                "target.txt",
                r"^missing$",
                "after",
                1,
                "missing anchor",
            )
            with self.assertRaises(pins.VersioningError):
                writes.build_write_plan(root, [update], [])
            self.assertEqual(target.read_text(), "before\n")

    def test_write_failure_rolls_back_all_files(self) -> None:
        """Restore the first destination when replacement of the second fails."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "first.txt"
            second = root / "second.txt"
            first.write_text("old one\n")
            second.write_text("old two\n")
            updates = [
                writes.AnchoredUpdate("first.txt", r"^old one$", "new one", 1, "first"),
                writes.AnchoredUpdate(
                    "second.txt", r"^old two$", "new two", 1, "second"
                ),
            ]
            plan = writes.build_write_plan(root, updates, [])
            replacements = 0

            def fail_second(source, destination):
                """Replace the first staged file and fail the second commit attempt."""

                nonlocal replacements
                replacements += 1
                if replacements == 2:
                    raise OSError("injected failure")
                os.replace(source, destination)

            with self.assertRaisesRegex(
                pins.VersioningError, "transactional write failed"
            ):
                writes.apply_write_plan(plan, replace=fail_second)
            self.assertEqual(first.read_text(), "old one\n")
            self.assertEqual(second.read_text(), "old two\n")

    def test_fence_failure_modifies_nothing(self) -> None:
        """Reject an incomplete synchronization fence without changing its file."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "target.md"
            target.write_text("# sync:example:start\nold\n")
            update = writes.FencedUpdate("target.md", "example", "new", "broken fence")
            with self.assertRaises(pins.VersioningError):
                writes.build_write_plan(root, [], [update])
            self.assertEqual(target.read_text(), "# sync:example:start\nold\n")


class GoModuleFreshnessTests(unittest.TestCase):
    """Check advertised and compatible Go graph updates without invoking Go."""

    def test_reports_direct_and_transitive_updates(self) -> None:
        """Preserve direct and indirect classifications when parsing Go update records."""

        documents = upstream._decode_json_stream(
            '{"Path":"example/direct","Version":"v1.0.0",'
            '"Update":{"Version":"v1.1.0"}}\n'
            '{"Path":"example/transitive","Version":"v2.0.0",'
            '"Indirect":true,"Update":{"Version":"v2.1.0"}}'
        )
        updates = upstream._module_updates(documents)
        self.assertEqual(
            [(item.path, item.indirect) for item in updates],
            [("example/direct", False), ("example/transitive", True)],
        )

    def test_reports_only_changes_selected_by_compatible_upgrade(self) -> None:
        """Report additions, removals, and upgrades selected by a candidate module graph."""

        current = (
            "module example.test/project\n\n"
            "require (\n"
            "\texample.test/direct v1.0.0\n"
            "\texample.test/removed v1.0.0 // indirect\n"
            ")\n"
        )
        candidate = (
            "module example.test/project\n\n"
            "require (\n"
            "\texample.test/direct v1.1.0\n"
            "\texample.test/new v1.0.0 // indirect\n"
            ")\n"
        )
        updates = upstream._compatible_module_updates(
            current,
            candidate,
        )
        self.assertEqual(
            [(item.path, item.current, item.latest, item.indirect) for item in updates],
            [
                (
                    "example.test/direct",
                    "v1.0.0",
                    "v1.1.0",
                    False,
                ),
                (
                    "example.test/new",
                    "(not required)",
                    "v1.0.0",
                    True,
                ),
                (
                    "example.test/removed",
                    "v1.0.0",
                    "(removed)",
                    True,
                ),
            ],
        )

    def test_go_list_timeout_is_an_environment_error(self) -> None:
        """Convert a bounded Go-list timeout into the command error reported by the CLI."""

        timeout = upstream.subprocess.TimeoutExpired(
            cmd=["go", "list"], timeout=upstream.GO_COMMAND_TIMEOUT
        )
        with (
            mock.patch.object(upstream.subprocess, "run", side_effect=timeout) as run,
            self.assertRaisesRegex(
                pins.VersioningError,
                "go list timed out",
            ),
        ):
            upstream.discover_module_updates(REPO_ROOT)
        self.assertEqual(
            run.call_args.kwargs["timeout"],
            upstream.GO_COMMAND_TIMEOUT,
        )

    def test_reports_higher_advertised_version_when_upgrade_is_constrained(
        self,
    ) -> None:
        """Show an upstream release that remains above the compatible selected version."""

        advertised = [
            upstream.ModuleUpdate(
                "example.test/module",
                "v1.0.0",
                "v1.3.0",
                False,
            )
        ]
        selected = [
            upstream.ModuleUpdate(
                "example.test/module",
                "v1.0.0",
                "v1.2.0",
                False,
            )
        ]
        self.assertEqual(
            cli._constrained_module_updates(advertised, selected),
            [
                upstream.ModuleUpdate(
                    "example.test/module",
                    "v1.2.0",
                    "v1.3.0",
                    False,
                )
            ],
        )


class GoModuleCandidateTests(unittest.TestCase):
    """Check isolated module upgrades, locked dependencies, and failure cleanup."""

    def setUp(self) -> None:
        """Create the module text, checksum text, and toolchain pin used by each case."""

        self.base_mod = "module example.test/project\n\ngo 1.26.3\n"
        self.base_sum = "example.test/module v1.0.0 h1:old\n"
        self.values = {
            "GO_VERSION": "1.26.5",
        }

    def _runner(self, *, fail_at: int | None = None):
        """Build a command callback that stages candidate module files or fails at one step."""

        calls = 0

        def run(command, **kwargs):
            """Count fake Go calls and write tidy output only to the alternate module files."""

            nonlocal calls
            calls += 1
            if calls == fail_at:
                return mock.Mock(
                    returncode=1,
                    stdout="",
                    stderr=f"injected failure {calls}",
                )
            if command[1:3] == ["mod", "tidy"]:
                modfile = Path(kwargs["env"]["GOFLAGS"].removeprefix("-modfile="))
                modfile.write_text(
                    "module example.test/project\n\n"
                    "go 1.99.0\n\n"
                    "require example.test/module v1.1.0\n"
                )
                modfile.with_suffix(".sum").write_text(
                    "example.test/module v1.1.0 h1:new\n"
                )
            return mock.Mock(returncode=0, stdout="", stderr="")

        return run

    def test_upgrades_all_modules_with_an_alternate_modfile(self) -> None:
        """Require alternate module files, verification, and an unchanged source repository."""

        with tempfile.TemporaryDirectory() as directory:
            repo_root = Path(directory)
            go_mod = repo_root / "go.mod"
            go_sum = repo_root / "go.sum"
            go_mod.write_text(self.base_mod)
            go_sum.write_text(self.base_sum)
            with mock.patch.object(
                upstream.subprocess,
                "run",
                side_effect=self._runner(),
            ) as run:
                candidate_mod, candidate_sum = upstream.build_go_module_candidates(
                    repo_root,
                    self.base_mod,
                    self.base_sum,
                    self.values,
                )

            self.assertIn("require example.test/module v1.1.0", candidate_mod)
            self.assertIn("go 1.26.5", candidate_mod)
            self.assertNotIn("toolchain", candidate_mod)
            self.assertEqual(
                candidate_sum,
                "example.test/module v1.1.0 h1:new\n",
            )
            self.assertEqual(go_mod.read_text(), self.base_mod)
            self.assertEqual(go_sum.read_text(), self.base_sum)
            commands = [call.args[0][1:] for call in run.call_args_list]
            self.assertEqual(
                commands,
                [
                    ["get", "-u", "-t", "./..."],
                    ["mod", "tidy", "-go=1.26.5"],
                    ["mod", "verify"],
                ],
            )
            for call in run.call_args_list:
                self.assertEqual(
                    call.kwargs["env"]["GOTOOLCHAIN"],
                    "go1.26.5",
                )
            for call in run.call_args_list[:2]:
                self.assertEqual(call.kwargs["cwd"], repo_root)
                self.assertIn("-modfile=", call.kwargs["env"]["GOFLAGS"])

    def test_retains_kubernetes_openapi_at_the_current_revision(self) -> None:
        """Require the candidate upgrade to restore the pinned kube-openapi revision."""

        base_mod = (
            "module example.test/project\n\n"
            "go 1.26.3\n\n"
            "require (\n"
            "\tk8s.io/kube-openapi v0.0.0-20260821135717-be32def86098 "
            "// indirect\n"
            ")\n"
        )
        with tempfile.TemporaryDirectory() as directory:
            repo_root = Path(directory)
            (repo_root / "go.mod").write_text(base_mod)
            (repo_root / "go.sum").write_text(self.base_sum)
            with mock.patch.object(
                upstream.subprocess,
                "run",
                side_effect=self._runner(),
            ) as run:
                upstream.build_go_module_candidates(
                    repo_root,
                    base_mod,
                    self.base_sum,
                    self.values,
                )

        commands = [call.args[0][1:] for call in run.call_args_list]
        self.assertEqual(
            commands,
            [
                ["get", "-u", "-t", "./..."],
                [
                    "get",
                    "k8s.io/kube-openapi@v0.0.0-20260821135717-be32def86098",
                ],
                ["mod", "tidy", "-go=1.26.5"],
                ["mod", "verify"],
            ],
        )

    def test_go_failures_leave_repository_module_files_unchanged(self) -> None:
        """Fail each Go upgrade step and require original module files to remain intact."""

        for fail_at in (1, 2, 3):
            with (
                self.subTest(fail_at=fail_at),
                tempfile.TemporaryDirectory() as directory,
            ):
                repo_root = Path(directory)
                go_mod = repo_root / "go.mod"
                go_sum = repo_root / "go.sum"
                go_mod.write_text(self.base_mod)
                go_sum.write_text(self.base_sum)
                with (
                    mock.patch.object(
                        upstream.subprocess,
                        "run",
                        side_effect=self._runner(fail_at=fail_at),
                    ),
                    self.assertRaisesRegex(
                        pins.VersioningError,
                        "injected failure",
                    ),
                ):
                    upstream.build_go_module_candidates(
                        repo_root,
                        self.base_mod,
                        self.base_sum,
                        self.values,
                    )
                self.assertEqual(go_mod.read_text(), self.base_mod)
                self.assertEqual(go_sum.read_text(), self.base_sum)


class ApplyPlanTests(unittest.TestCase):
    """Check shared snapshots and rollback across pins, modules, and derived files."""

    def test_reuses_go_sum_snapshot_for_candidate_and_rollback(self) -> None:
        """Require one checksum snapshot even when the source changes during planning."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            go_mod = root / "go.mod"
            go_sum = root / "go.sum"
            versions_env = root / "hack/versions.env"
            versions_env.parent.mkdir()
            go_mod.write_text("old mod\n")
            go_sum.write_text("old sum\n")
            versions_env.write_text("old env\n")
            sync_plan = writes.WritePlan(
                root=root,
                originals={go_mod: "old mod\n"},
                candidates={go_mod: "synced mod\n"},
            )

            def build_candidates(
                repo_root,
                candidate_mod,
                base_sum,
                candidate_values,
            ):
                """Check snapshot inputs and change the source checksum after it was captured."""

                self.assertEqual(repo_root, root)
                self.assertEqual(candidate_mod, "synced mod\n")
                self.assertEqual(base_sum, "old sum\n")
                self.assertEqual(
                    candidate_values,
                    {
                        "GO_VERSION": "1.26.5",
                    },
                )
                go_sum.write_text("changed after snapshot\n")
                return "new mod\n", "new sum\n"

            with (
                mock.patch.object(
                    cli,
                    "_sync_plan",
                    return_value=sync_plan,
                ),
                mock.patch.object(
                    upstream,
                    "build_go_module_candidates",
                    side_effect=build_candidates,
                ),
            ):
                plan = cli._complete_apply_plan(
                    root,
                    "new env\n",
                    {
                        "GO_VERSION": "1.26.5",
                    },
                )

            self.assertEqual(plan.originals[go_sum], "old sum\n")
            self.assertEqual(plan.candidates[go_sum], "new sum\n")
            self.assertEqual(go_sum.read_text(), "changed after snapshot\n")

    def test_combines_and_rolls_back_env_modules_and_derived_files(self) -> None:
        """Restore all committed destinations when a combined apply fails partway through."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            derived = root / "derived.txt"
            go_mod = root / "go.mod"
            go_sum = root / "go.sum"
            versions_env = root / "hack/versions.env"
            versions_env.parent.mkdir()
            derived.write_text("old derived\n")
            go_mod.write_text("old mod\n")
            go_sum.write_text("old sum\n")
            versions_env.write_text("old env\n")
            sync_plan = writes.WritePlan(
                root=root,
                originals={
                    derived: "old derived\n",
                    go_mod: "old mod\n",
                },
                candidates={
                    derived: "new derived\n",
                    go_mod: "synced mod\n",
                },
            )
            with (
                mock.patch.object(
                    cli,
                    "_sync_plan",
                    return_value=sync_plan,
                ),
                mock.patch.object(
                    upstream,
                    "build_go_module_candidates",
                    return_value=("new mod\n", "new sum\n"),
                ),
            ):
                plan = cli._complete_apply_plan(
                    root,
                    "new env\n",
                    {
                        "GO_VERSION": "1.26.5",
                    },
                )

            self.assertEqual(
                set(plan.changed_paths),
                {derived, go_mod, go_sum, versions_env},
            )
            replacements = 0

            def fail_third(source, destination):
                """Commit two staged destinations and fail the third replacement."""

                nonlocal replacements
                replacements += 1
                if replacements == 3:
                    raise OSError("injected apply failure")
                os.replace(source, destination)

            with self.assertRaisesRegex(
                pins.VersioningError,
                "transactional write failed",
            ):
                writes.apply_write_plan(plan, replace=fail_third)

            self.assertEqual(derived.read_text(), "old derived\n")
            self.assertEqual(go_mod.read_text(), "old mod\n")
            self.assertEqual(go_sum.read_text(), "old sum\n")
            self.assertEqual(versions_env.read_text(), "old env\n")


class SynchronizerTests(unittest.TestCase):
    """Check canonical rendering, drift detection, and repository-location ownership."""

    @classmethod
    def setUpClass(cls) -> None:
        """Build the canonical replacement groups shared by synchronization cases."""

        cls.values = pins.parse_versions_env(HACK_DIR / "versions.env")
        cls.anchored, cls.fenced = rendering.build_updates(cls.values, REPO_ROOT)

    def _copy_sync_surface(self, root: Path) -> None:
        """Copy every derived target into a disposable repository fixture."""

        relative_paths = {update.rel_path for update in [*self.anchored, *self.fenced]}
        for relative in relative_paths:
            source = REPO_ROOT / relative
            destination = root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)

    def test_repository_is_idempotently_synchronized(self) -> None:
        """Require canonical pins to produce no changes to checked-in derived files."""

        plan = writes.build_write_plan(REPO_ROOT, self.anchored, self.fenced)
        self.assertEqual(plan.changed_paths, ())

    def test_drift_detection_and_check_plan_never_write(self) -> None:
        """Report an outdated Go directive without writing its destination."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._copy_sync_surface(root)
            go_mod = root / "go.mod"
            original = go_mod.read_text().replace(
                f"go {self.values['GO_VERSION']}", "go 1.25.0"
            )
            go_mod.write_text(original)
            plan = writes.build_write_plan(root, self.anchored, self.fenced)
            self.assertIn(go_mod, plan.changed_paths)
            self.assertEqual(go_mod.read_text(), original)

    def test_exact_go_directive_rendering(self) -> None:
        """Require the pinned Go directive and the absence of a separate toolchain directive."""

        go_mod = (REPO_ROOT / "go.mod").read_text()
        self.assertIn(f"go {self.values['GO_VERSION']}\n", go_mod)
        self.assertNotIn("toolchain go", go_mod)

    def test_mise_tool_pins_render_from_canonical_versions(self) -> None:
        """Plan a Mise pin repair without writing the fixture during drift detection."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._copy_sync_surface(root)
            mise_toml = root / "mise.toml"
            original = mise_toml.read_text().replace(
                f'go = "{self.values["GO_VERSION"]}"', 'go = "1.25.0"'
            )
            mise_toml.write_text(original)

            plan = writes.build_write_plan(root, self.anchored, self.fenced)

            self.assertIn(mise_toml, plan.changed_paths)
            self.assertEqual(mise_toml.read_text(), original)
            self.assertIn(
                f'go = "{self.values["GO_VERSION"]}"',
                plan.candidates[mise_toml],
            )

    def test_synchronizer_does_not_embed_repository_locations(self) -> None:
        """Keep canonical repository locations out of the version-tool implementation."""

        source = "\n".join(
            path.read_text()
            for path in sorted(
                [HACK_DIR / "versions.py", *(HACK_DIR / "version_tool").glob("*.py")]
            )
        )
        repositories = {
            value
            for key, value in self.values.items()
            if key.endswith("_IMAGE_REPOSITORY")
        }
        self.assertTrue(repositories)
        for repository in repositories:
            with self.subTest(repository=repository):
                self.assertNotIn(repository, source)

    def test_every_repository_key_is_consumed(self) -> None:
        """Require each canonical image repository to feed rendering or source discovery."""

        source = "\n".join(
            path.read_text()
            for path in sorted(
                [HACK_DIR / "versions.py", *(HACK_DIR / "version_tool").glob("*.py")]
            )
        )
        source_ids = "\n".join(spec.source_id for spec in pins.SPECS if spec.source_id)
        repository_keys = {
            key for key in self.values if key.endswith("_IMAGE_REPOSITORY")
        }
        for key in repository_keys:
            with self.subTest(key=key):
                self.assertTrue(
                    key in source or f"{{{key}}}" in source_ids,
                    f"{key} is not consumed by sync or freshness tooling",
                )


class TargetModuleTests(unittest.TestCase):
    """Check that extra update targets are found at a fixed depth and are optional."""

    @classmethod
    def setUpClass(cls) -> None:
        """Load the canonical values once."""

        cls.values = pins.parse_versions_env(HACK_DIR / "versions.env")

    def _write_module(self, root: Path, relative: str, source: str) -> Path:
        """Write a target module made of the given source text."""

        module = root / relative
        module.parent.mkdir(parents=True, exist_ok=True)
        module.write_text(textwrap.dedent(source))
        return module

    def _pinning_module(self, target: str) -> str:
        """Return module source that pins one line of one fixture file."""

        return textwrap.dedent(
            f"""\
            from version_tool.writes import AnchoredUpdate


            def updates(values):
                return [
                    AnchoredUpdate(
                        rel_path={target!r},
                        pattern=r'^(  GO: )"[^"]+"$',
                        replacement=rf'\\g<1>"{{values["GO_VERSION"]}}"',
                        expect=1,
                        desc="fixture GO",
                    )
                ]
            """
        )

    def test_module_is_found_and_its_updates_are_used(self) -> None:
        """Add a discovered module's updates and rewrite its target file."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            module = self._write_module(
                root,
                "pkg/ci/version-sync-targets.py",
                self._pinning_module("pkg/config.yml"),
            )
            (root / "pkg" / "config.yml").write_text('  GO: "1.0.0"\n')

            anchored, _ = rendering.build_updates(self.values, root)
            plan = writes.build_write_plan(
                root, [u for u in anchored if u.rel_path == "pkg/config.yml"], []
            )

            self.assertEqual(rendering.discover_target_modules(root), [module])
            self.assertEqual(
                plan.candidates[root / "pkg" / "config.yml"],
                f'  GO: "{self.values["GO_VERSION"]}"\n',
            )

    def test_nested_checkout_module_is_not_found(self) -> None:
        """Ignore a module inside a nested checkout below the repository root."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._write_module(
                root,
                "nested/checkout/pkg/ci/version-sync-targets.py",
                "raise RuntimeError('nested checkout must be ignored')\n",
            )

            self.assertEqual(rendering.discover_target_modules(root), [])
            rendering.build_updates(self.values, root)

    def test_dot_directory_module_is_found(self) -> None:
        """Find a module whose top-level directory name starts with a dot."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            module = self._write_module(
                root,
                ".hidden/ci/version-sync-targets.py",
                "def updates(values):\n    return []\n",
            )

            self.assertEqual(rendering.discover_target_modules(root), [module])

    def test_modules_are_returned_in_sorted_path_order(self) -> None:
        """Return modules in the same order on every machine."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = "def updates(values):\n    return []\n"
            second = self._write_module(root, "b/ci/version-sync-targets.py", source)
            first = self._write_module(root, "a/ci/version-sync-targets.py", source)

            self.assertEqual(rendering.discover_target_modules(root), [first, second])

    def test_checkout_without_modules_has_no_extra_targets(self) -> None:
        """Build the usual updates when no target module exists."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            without, _ = rendering.build_updates(self.values, root)
            self._write_module(
                root,
                "pkg/ci/version-sync-targets.py",
                self._pinning_module("pkg/config.yml"),
            )
            with_one, _ = rendering.build_updates(self.values, root)

            self.assertEqual(rendering.discover_target_modules(root / "empty"), [])
            self.assertEqual(len(with_one), len(without) + 1)

    def test_module_without_updates_function_is_rejected(self) -> None:
        """Fail clearly instead of ignoring a malformed target module."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._write_module(root, "pkg/ci/version-sync-targets.py", "x = 1\n")

            with self.assertRaisesRegex(
                pins.VersioningError, "version-sync-targets.py"
            ):
                rendering.build_updates(self.values, root)

    def test_module_that_raises_is_reported_by_name(self) -> None:
        """Wrap a module's own failure in a versioning error that names the module."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._write_module(
                root,
                "pkg/ci/version-sync-targets.py",
                """\
                def updates(values):
                    raise KeyError("MISSING_PIN")
                """,
            )

            with self.assertRaisesRegex(
                pins.VersioningError, r"ci/version-sync-targets\.py: .*MISSING_PIN"
            ):
                rendering.build_updates(self.values, root)

    def test_module_returning_a_non_update_is_reported_by_name(self) -> None:
        """Wrap a malformed update item in a versioning error naming the module."""

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._write_module(
                root,
                "pkg/ci/version-sync-targets.py",
                """\
                def updates(values):
                    return [None]
                """,
            )

            with self.assertRaisesRegex(
                pins.VersioningError,
                r"^pkg/ci/version-sync-targets\.py: .*not an AnchoredUpdate: None$",
            ):
                rendering.build_updates(self.values, root)

    def test_update_outside_repository_is_rejected(self) -> None:
        """Reject absolute paths and paths that climb out of the repository root."""

        for target in ("/etc/hosts", "../outside.yml"):
            with (
                self.subTest(target=target),
                tempfile.TemporaryDirectory() as directory,
            ):
                root = Path(directory)
                self._write_module(
                    root, "pkg/ci/version-sync-targets.py", self._pinning_module(target)
                )

                with self.assertRaisesRegex(
                    pins.VersioningError, "outside the repository"
                ):
                    rendering.build_updates(self.values, root)


if __name__ == "__main__":
    unittest.main()
