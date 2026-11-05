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
        for v in ["0.1.0", "1.0.0", "10.20.30", "0.2.0-rc.1", "1.0.0-alpha-1.x"]:
            with self.subTest(v=v):
                self.assertEqual(release.check_form(v), v)

    def test_other_versions_are_refused(self) -> None:
        for v in ["v0.1.0", "0.1", "0.1.0.0", "01.1.0", "0.1.0+build.1", "0.1.0-", "0.1.0-rc..1", "", None, 1]:
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
    """The engine that build-engine builds, as the release builds it,
    prints the package's version: the engine and the npm package cannot
    differ."""

    @unittest.skipUnless(shutil.which("go"), "needs the go command and a C compiler")
    def test_the_release_engine_prints_the_package_version(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "windmill-lite")
            cli = pathlib.Path(__file__).with_name("release.py")
            subprocess.run([sys.executable, str(cli), "build-engine", out], check=True)
            printed = subprocess.run([out, "version"], capture_output=True, text=True, check=True).stdout
            self.assertEqual(printed, f"windmill-lite v{release.version()}\n")
            subprocess.run([sys.executable, str(cli), "check-engine", out], check=True)


if __name__ == "__main__":
    unittest.main()
