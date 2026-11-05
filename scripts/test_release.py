"""Tests for release.py. Run: python3 -m unittest discover -s scripts"""

from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest

_spec = importlib.util.spec_from_file_location(
    "release", pathlib.Path(__file__).with_name("release.py")
)
release = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(release)


class VersionTest(unittest.TestCase):
    def test_the_package_version_is_a_release_version(self) -> None:
        with open(release.PACKAGE_JSON, encoding="utf-8") as f:
            declared = json.load(f)["version"]
        self.assertEqual(release.version(), declared)

    def test_release_versions(self) -> None:
        for v in ["0.1.0", "1.0.0", "10.20.30", "0.2.0-rc.1", "1.0.0-alpha-1.x", "1.0.0-0", "1.0.0-0a.10"]:
            with self.subTest(v=v):
                self.assertEqual(release.check_form(v), v)

    def test_other_versions_are_refused(self) -> None:
        for v in ["v0.1.0", "0.1", "0.1.0.0", "01.1.0", "0.1.0+build.1", "0.1.0-", "0.1.0-rc..1", "0.1.0-01", "0.1.0-rc.01", "", None, 1]:
            with self.subTest(v=v):
                with self.assertRaises(release.ReleaseError):
                    release.check_form(v)

    def test_version_reads_the_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / "package.json"
            p.write_text('{"name": "windmill-lite", "version": "3.4.5"}')
            self.assertEqual(release.version(p), "3.4.5")
            p.write_text('{"name": "windmill-lite"}')
            with self.assertRaises(release.ReleaseError):
                release.version(p)


class TagTest(unittest.TestCase):
    def test_the_tag_is_v_and_the_version(self) -> None:
        release.check_tag("v0.1.0", "0.1.0")

    def test_another_tag_is_refused(self) -> None:
        for given in ["0.1.0", "v0.1.1", "v0.1.0-rc.1", "refs/tags/v0.1.0", "V0.1.0"]:
            with self.subTest(given=given):
                with self.assertRaises(release.ReleaseError):
                    release.check_tag(given, "0.1.0")


class ModeTest(unittest.TestCase):
    """Which runs of the release workflow may release, and which are dry
    runs or refused."""

    UP = "invakid404/windmill-lite"
    FORK = "someone/windmill-lite"

    def mode(self, event: str, ref_type: str, ref_name: str, repository: str, dry_run_input: str = "") -> bool:
        return release.release_mode(event, ref_type, ref_name, repository, dry_run_input, "0.1.0")

    def test_the_upstream_tag_releases(self) -> None:
        self.assertFalse(self.mode("push", "tag", "v0.1.0", self.UP))

    def test_another_tag_is_refused(self) -> None:
        for repository in (self.UP, self.FORK):
            with self.subTest(repository=repository), self.assertRaises(release.ReleaseError):
                self.mode("push", "tag", "v0.1.1", repository)

    def test_a_fork_tag_is_a_dry_run(self) -> None:
        self.assertTrue(self.mode("push", "tag", "v0.1.0", self.FORK))

    def test_pull_requests_are_dry_runs(self) -> None:
        for repository in (self.UP, self.FORK):
            with self.subTest(repository=repository):
                self.assertTrue(self.mode("pull_request", "branch", "98/merge", repository))

    def test_a_dispatch_is_a_dry_run_by_default(self) -> None:
        for repository, ref_type, ref_name in [(self.UP, "branch", "main"), (self.UP, "tag", "v0.1.0"), (self.FORK, "branch", "main"), (self.FORK, "tag", "v0.1.0")]:
            for dry_run_input in ("true", ""):
                with self.subTest(repository=repository, ref=ref_name, dry_run=dry_run_input):
                    self.assertTrue(self.mode("workflow_dispatch", ref_type, ref_name, repository, dry_run_input))

    def test_a_dispatch_releases_only_upstream_on_the_tag(self) -> None:
        self.assertFalse(self.mode("workflow_dispatch", "tag", "v0.1.0", self.UP, "false"))
        for repository, ref_type, ref_name in [(self.UP, "branch", "main"), (self.FORK, "tag", "v0.1.0"), (self.FORK, "branch", "main"), (self.UP, "tag", "v0.1.1")]:
            with self.subTest(repository=repository, ref=ref_name), self.assertRaises(release.ReleaseError):
                self.mode("workflow_dispatch", ref_type, ref_name, repository, "false")

    def test_other_events_are_refused(self) -> None:
        for event, ref_type in [("push", "branch"), ("schedule", "branch"), ("pull_request_target", "branch"), ("", "")]:
            with self.subTest(event=event), self.assertRaises(release.ReleaseError):
                self.mode(event, ref_type, "main", self.UP)

    def test_the_command_reads_the_actions_environment(self) -> None:
        cli = pathlib.Path(__file__).with_name("release.py")
        v = release.version()
        base = {"PATH": os.environ.get("PATH", ""), "GITHUB_REPOSITORY": self.UP}
        cases = [
            ({"GITHUB_EVENT_NAME": "push", "GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": f"v{v}"}, 0, "dry_run=false\n"),
            ({"GITHUB_EVENT_NAME": "pull_request", "GITHUB_REF_TYPE": "branch", "GITHUB_REF_NAME": "98/merge"}, 0, "dry_run=true\n"),
            ({"GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF_TYPE": "branch", "GITHUB_REF_NAME": "main", "DRY_RUN_INPUT": "false"}, 1, ""),
        ]
        for env, code, out in cases:
            with self.subTest(env=env):
                res = subprocess.run([sys.executable, str(cli), "mode"], env={**base, **env}, capture_output=True, text=True)
                self.assertEqual((res.returncode, res.stdout), (code, out), res.stderr)


class CheckEngineTest(unittest.TestCase):
    def fake(self, d: str, line: str, code: int = 0) -> str:
        p = pathlib.Path(d) / "windmill-lite"
        p.write_text(f"#!/bin/sh\nprintf '%s\\n' '{line}'\nexit {code}\n")
        p.chmod(p.stat().st_mode | stat.S_IXUSR)
        return str(p)

    def test_an_engine_of_the_version_passes(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            release.check_engine(self.fake(d, "windmill-lite v0.1.0"), "0.1.0")

    def test_an_engine_of_another_version_fails(self) -> None:
        for line in ["windmill-lite v0.1.1", "windmill-lite 0.1.0", "windmill-lite (devel)", "windmill-lite v0.1.0 extra"]:
            with self.subTest(line=line), tempfile.TemporaryDirectory() as d:
                with self.assertRaises(release.ReleaseError):
                    release.check_engine(self.fake(d, line), "0.1.0")

    def test_a_failing_engine_fails(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(release.ReleaseError):
                release.check_engine(self.fake(d, "windmill-lite v0.1.0", 1), "0.1.0")


class ReleaseEngineTest(unittest.TestCase):
    """The engine that build-engine builds, with the release's version
    stamp, prints the package's version: the release engine and the npm
    package cannot differ."""

    @unittest.skipUnless(shutil.which("go"), "needs the go command and a C compiler")
    def test_the_release_engine_prints_the_package_version(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "windmill-lite")
            cli = pathlib.Path(__file__).with_name("release.py")
            subprocess.run([sys.executable, str(cli), "build-engine", out], check=True)
            printed = subprocess.run([out, "version"], capture_output=True, text=True, check=True).stdout
            self.assertEqual(printed, f"windmill-lite v{release.version()}\n")
            subprocess.run([sys.executable, str(cli), "check-engine", out], check=True)


class NoticesTest(unittest.TestCase):
    """The licence texts of the release assets name every third-party part
    of the engine binary."""

    @unittest.skipUnless(shutil.which("go"), "needs the go command")
    def test_the_notices_cover_the_engine(self) -> None:
        text = release.notices()
        for title, files in release.VENDORED_NOTICES:
            self.assertIn(title, text)
            for f in files:
                self.assertIn(f"--- {f} ---", text)
        modules = [path for path, _, _ in release.go_modules()]
        self.assertIn("github.com/evanw/esbuild", modules)
        for path in modules:
            self.assertIn(f"Go module {path} ", text)
        self.assertIn("This is free and unencumbered software released into the public domain.", text)
        self.assertIn("Go (the standard library and runtime)", text)


if __name__ == "__main__":
    unittest.main()
