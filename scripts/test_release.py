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
from unittest import mock

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


class MPLSourceTest(unittest.TestCase):
    """The notice of a Go module under the Mozilla Public License 2.0 says
    where its source is, as section 3.2 of the licence asks of a work
    distributed in executable form. The modules are synthetic, and each test
    names its own MPL_MODULES, so the tests do not depend on which modules
    the engine links."""

    MPL = "Copyright (c) 2026 Example\n\nMozilla Public License, version 2.0\n\n1. Definitions\n"
    MIT = "MIT License\n\nCopyright (c) 2026 Example\n\nPermission is hereby granted, free of charge, ...\n"
    APACHE = "Apache License\nVersion 2.0, January 2004\nhttp://www.apache.org/licenses/\n\nTERMS AND CONDITIONS\n"

    def module(self, d: str, name: str, files: dict[str, str]) -> str:
        directory = pathlib.Path(d) / name
        directory.mkdir()
        for file, text in files.items():
            (directory / file).write_text(text, encoding="utf-8")
        return str(directory)

    def mpl_modules(self, *paths: str):
        return mock.patch.object(release, "MPL_MODULES", frozenset(paths))

    def test_the_mpl_modules_are_go_plugin_and_yamux(self) -> None:
        self.assertEqual(release.MPL_MODULES, {"github.com/hashicorp/go-plugin", "github.com/hashicorp/yamux"})

    def test_notices_mpl_source(self) -> None:
        with tempfile.TemporaryDirectory() as d, self.mpl_modules("github.com/Example/MPL-Thing/v2"):
            text = release.module_notice("github.com/Example/MPL-Thing/v2", "v2.1.0-RC.1", self.module(d, "mpl", {"LICENSE": self.MPL}))
        source = (
            "Source Code Form: the source of github.com/Example/MPL-Thing/v2 v2.1.0-RC.1 is available under the "
            "Mozilla Public License 2.0 from https://proxy.golang.org/github.com/!example/!m!p!l-!thing/v2/@v/v2.1.0-!r!c.1.zip "
            "and from its upstream repository https://github.com/Example/MPL-Thing.\n"
        )
        self.assertIn("Go module github.com/Example/MPL-Thing/v2 v2.1.0-RC.1\n", text)
        self.assertIn(source, text)
        self.assertIn(self.MPL, text)
        self.assertLess(text.index(source), text.index("--- LICENSE ---"))

    def test_an_spdx_identifier_names_the_mpl(self) -> None:
        with tempfile.TemporaryDirectory() as d, self.mpl_modules("example.com/x/mpl"):
            text = release.module_notice("example.com/x/mpl", "v0.3.0", self.module(d, "mpl", {"LICENSE": "// SPDX-License-Identifier: MPL-2.0\n\nCopyright 2026 Example\n"}))
        self.assertIn("from https://proxy.golang.org/example.com/x/mpl/@v/v0.3.0.zip and from its upstream repository https://example.com/x/mpl.", text)

    def test_other_licences_get_no_source_line(self) -> None:
        with tempfile.TemporaryDirectory() as d, self.mpl_modules():
            text = release.module_notice("github.com/example/mit", "v1.0.0", self.module(d, "mit", {"LICENSE": self.MIT, "NOTICE": "Mozilla Firefox users, take note\n"}))
        self.assertNotIn("Source Code Form", text)
        self.assertIn(self.MIT, text)

    # Licence files of modules outside MPL_MODULES that mention the MPL: an
    # attribution in a NOTICE, one further down a LICENSE, and a component's
    # SPDX line in a LICENSE.
    ATTRIBUTION = "\n\nThe component x is available under the Mozilla Public License, version 2.0.\n"
    COMPONENT = "\n\n--- third_party/x ---\n\nSPDX-License-Identifier: MPL-2.0\n"
    FILLER = "\n".join(f"Section {i}." for i in range(10))
    MENTIONS = {
        "in its NOTICE": {"LICENSE": APACHE, "NOTICE": "Example\nCopyright 2026 Example" + ATTRIBUTION},
        "further down its LICENSE": {"LICENSE": APACHE + FILLER + ATTRIBUTION},
        "as a component's SPDX line in its LICENSE": {"LICENSE": APACHE + FILLER + COMPONENT},
        "in the NOTICE of an MIT module": {"LICENSE": MIT, "NOTICE": "This module contains no code under the MPL-2.0.\n"},
    }

    def test_an_other_module_that_mentions_the_mpl_keeps_its_texts(self) -> None:
        """A module outside MPL_MODULES whose licence files mention the MPL
        gets no source line, and its section holds every licence and notice
        text as it is."""
        for name, files in self.MENTIONS.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as d, self.mpl_modules("github.com/example/listed"):
                text = release.module_notice("github.com/example/apache", "v1.2.0", self.module(d, "apache", files))
            self.assertNotIn("Source Code Form", text)
            self.assertIn("Go module github.com/example/apache v1.2.0\n", text)
            for file, body in files.items():
                self.assertIn(f"--- {file} ---\n\n{body}", text)

    @unittest.skipUnless(shutil.which("go"), "needs the go command")
    def test_notices_of_a_module_that_attributes_the_mpl(self) -> None:
        """The whole output of notices, with module discovery returning a
        listed MPL-2.0 module and a module that attributes an MPL-2.0
        component: the source line only in the listed module's section, and
        the other module's texts complete."""
        files = self.MENTIONS["as a component's SPDX line in its LICENSE"] | {"NOTICE": "Example" + self.ATTRIBUTION}
        with tempfile.TemporaryDirectory() as d:
            modules = [
                ("github.com/example/apache", "v1.2.0", self.module(d, "apache", files)),
                ("github.com/example/mpl", "v1.0.0", self.module(d, "mpl", {"LICENSE": self.MPL})),
            ]
            with mock.patch.object(release, "go_modules", return_value=modules), self.mpl_modules("github.com/example/mpl"):
                text = release.notices()
        apache = text.index("Go module github.com/example/apache v1.2.0\n")
        mpl = text.index("Go module github.com/example/mpl v1.0.0\n")
        self.assertEqual(text.count("Source Code Form"), 1)
        self.assertGreater(text.index("Source Code Form"), mpl)
        for file, body in files.items():
            self.assertIn(f"--- {file} ---\n\n{body}", text[apache:mpl])
        self.assertIn("Go (the standard library and runtime)", text)

    def test_a_listed_module_under_another_licence_fails(self) -> None:
        """A module of MPL_MODULES gets the source line only while its own
        licence file is the MPL-2.0, not one that names the MPL further down."""
        filler = "\n".join(f"Section {i}." for i in range(10))
        for name, files in {
            "Apache-2.0": {"LICENSE": self.APACHE},
            "Apache-2.0 with an MPL-2.0 component": {"LICENSE": self.APACHE + filler + "\n\nSPDX-License-Identifier: MPL-2.0\n"},
            "MPL-2.0 in its NOTICE only": {"LICENSE": self.MIT, "NOTICE": self.MPL},
        }.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as d, self.mpl_modules("github.com/example/listed"):
                with self.assertRaisesRegex(release.ReleaseError, "is in MPL_MODULES"):
                    release.module_notice("github.com/example/listed", "v1.0.0", self.module(d, "listed", files))

    @unittest.skipUnless(shutil.which("go"), "needs the go command")
    def test_notices_carry_the_source_line(self) -> None:
        """The notices of a release name the source of an MPL-2.0 module that
        the engine links, here a synthetic one that module discovery
        returns."""
        with tempfile.TemporaryDirectory() as d:
            mpl = self.module(d, "mpl", {"LICENSE": self.MPL})
            mit = self.module(d, "mit", {"LICENSE": self.MIT})
            modules = [("github.com/example/mpl", "v1.0.0", mpl), ("github.com/example/mit", "v1.1.0", mit)]
            with mock.patch.object(release, "go_modules", return_value=modules), self.mpl_modules("github.com/example/mpl"):
                text = release.notices()
        source = (
            "Source Code Form: the source of github.com/example/mpl v1.0.0 is available under the "
            "Mozilla Public License 2.0 from https://proxy.golang.org/github.com/example/mpl/@v/v1.0.0.zip "
            "and from its upstream repository https://github.com/example/mpl."
        )
        mpl_section = text.index("Go module github.com/example/mpl v1.0.0\n")
        mit_section = text.index("Go module github.com/example/mit v1.1.0\n")
        self.assertEqual(text.count(source), 1)
        self.assertTrue(mpl_section < text.index(source) < mit_section, "the line is not in the MPL module's section")
        self.assertIn("Go (the standard library and runtime)", text)

    def test_a_module_without_a_licence_file_fails(self) -> None:
        with tempfile.TemporaryDirectory() as d, self.assertRaises(release.ReleaseError):
            release.module_notice("github.com/example/none", "v1.0.0", self.module(d, "none", {"README.md": "no licence\n"}))


if __name__ == "__main__":
    unittest.main()
