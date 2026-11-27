"""Tests for check-bucket-audit.py. Run: python3 -m unittest discover -s scripts

The audit outputs below have the exact form of testbucket v0.2.2's audit
(internal/core/audit.go): the report on standard output, then the error on
standard error. The events have the form of `go test -json` output.
"""

from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import stat
import subprocess
import sys
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("check-bucket-audit.py")
_spec = importlib.util.spec_from_file_location("check_bucket_audit", SCRIPT)
rule = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(rule)

PKG = "example.com/m/cmd/tool"
OTHER = "example.com/m/internal/lib"

PASSED = """testbucket audit — 3 planned unit(s) over 2 package(s)
  packages that reported a result   2
  run-sliced packages name-checked  1

PASS — every planned package reported exactly the invocations the plan
scheduled for it, counting a count-shard group as the one logical package
it is, and every -run slice's names are accounted for.
"""

TRAILER = """
The plan's coverage gate proves the MATRIX is complete before anything
runs. This proves the RUN was. A bucket that produced no events is
invisible to the gate and is exactly what this catches.
"""


def failed(*groups: tuple[str, list[str]]) -> str:
    """A failed audit's output with these discrepancy groups."""
    out = (
        "testbucket audit — 3 planned unit(s) over 2 package(s)\n"
        "  packages that reported a result   2\n"
        "  run-sliced packages name-checked  1\n"
        "\n"
        "testbucket: coverage audit FAILED: the run did not execute what the plan scheduled"
    )
    for label, items in groups:
        out += f"\n  {label}:"
        for item in items:
            out += f"\n    - {item}"
    return out + "\n" + TRAILER


def never(pkg: str, name: str) -> tuple[str, list[str]]:
    return ("-run slice discrepancies", [f"{pkg}: {name} was in a -run slice but never reported"])


def events(*records: tuple[str, str, str]) -> str:
    """go test -json lines: (action, package, test), each with its run event."""
    lines = []
    for action, pkg, test in records:
        if test:
            lines.append(json.dumps({"Action": "run", "Package": pkg, "Test": test}))
        lines.append(json.dumps({"Action": action, "Package": pkg, "Test": test, "Elapsed": 0.01}))
    return "\n".join(lines) + "\n"


SKIPPED = events(("pass", PKG, "TestA"), ("skip", PKG, "TestRedis"), ("pass", PKG, ""))


class DecideTest(unittest.TestCase):
    def test_a_passing_audit_passes(self) -> None:
        self.assertEqual(rule.decide(0, PASSED, [SKIPPED]), [])

    def test_a_skipped_test_is_accepted(self) -> None:
        out = failed(never(PKG, "TestRedis"))
        self.assertEqual(rule.decide(1, out, [SKIPPED]), [(PKG, "TestRedis")])

    def test_skips_in_several_event_files_are_accepted(self) -> None:
        out = failed(("-run slice discrepancies", [
            f"{PKG}: TestRedis was in a -run slice but never reported",
            f"{PKG}: TestRedis2 was in a -run slice but never reported",
        ]))
        second = events(("skip", PKG, "TestRedis2"), ("pass", PKG, ""))
        self.assertEqual(rule.decide(1, out, [SKIPPED, second]), [(PKG, "TestRedis"), (PKG, "TestRedis2")])

    def test_a_test_that_did_not_skip_fails(self) -> None:
        out = failed(never(PKG, "TestGone"))
        with self.assertRaisesRegex(rule.RuleError, "did not skip: example.com/m/cmd/tool: TestGone"):
            rule.decide(1, out, [SKIPPED])

    def test_a_skip_in_another_package_does_not_count(self) -> None:
        out = failed(never(PKG, "TestRedis"))
        with self.assertRaises(rule.RuleError):
            rule.decide(1, out, [events(("skip", OTHER, "TestRedis"), ("pass", PKG, ""))])

    def test_a_skipped_subtest_does_not_count(self) -> None:
        out = failed(never(PKG, "TestRedis"))
        with self.assertRaises(rule.RuleError):
            rule.decide(1, out, [events(("skip", PKG, "TestRedis/sub"), ("pass", PKG, ""))])
        out = failed(never(PKG, "TestRedis/sub"))
        with self.assertRaisesRegex(rule.RuleError, "not a top-level test"):
            rule.decide(1, out, [events(("skip", PKG, "TestRedis/sub"), ("pass", PKG, ""))])

    def test_another_kind_of_discrepancy_fails(self) -> None:
        for label, item in (
            ("package(s) that never reported — a bucket produced no events", f"{OTHER} (planned 1 invocation(s), reported none)"),
            ("package(s) that reported fewer invocations than planned — a shard or slice is missing", f"{PKG} reported 1 of 2 planned invocation(s)"),
            ("package(s) that reported more invocations than planned", f"{PKG} reported 3 invocation(s), 2 were planned"),
            ("package(s) that ran but were in no bucket", f"{OTHER} reported 1 result(s) but was in no bucket"),
            ("-run slice discrepancies", f"{PKG}: TestB reported but was in no -run slice"),
        ):
            with self.subTest(label=label), self.assertRaisesRegex(rule.RuleError, "not a skipped test"):
                rule.decide(1, failed((label, [item])), [SKIPPED])

    def test_a_skip_mixed_with_a_missing_test_fails(self) -> None:
        out = failed(("-run slice discrepancies", [
            f"{PKG}: TestRedis was in a -run slice but never reported",
            f"{PKG}: TestGone was in a -run slice but never reported",
        ]))
        with self.assertRaisesRegex(rule.RuleError, "TestGone"):
            rule.decide(1, out, [SKIPPED])

    def test_a_skip_mixed_with_another_kind_fails(self) -> None:
        out = failed(
            ("package(s) that never reported — a bucket produced no events", [f"{OTHER} (planned 1 invocation(s), reported none)"]),
            never(PKG, "TestRedis"),
        )
        with self.assertRaisesRegex(rule.RuleError, "not a skipped test"):
            rule.decide(1, out, [SKIPPED])
        out = failed(("-run slice discrepancies", [
            f"{PKG}: TestRedis was in a -run slice but never reported",
            f"{PKG}: TestB reported but was in no -run slice",
        ]))
        with self.assertRaisesRegex(rule.RuleError, "not a skipped test"):
            rule.decide(1, out, [SKIPPED])

    def test_unknown_output_fails(self) -> None:
        for out in (
            "testbucket: read shard plan: open plan.json: no such file or directory\n",
            "testbucket: no `go test -json` package results found (3 lines, 0 events, 3 unparsable)\n",
            failed(never(PKG, "TestRedis")) + "an extra line\n",
            failed(),
            "",
        ):
            with self.subTest(out=out), self.assertRaises(rule.RuleError):
                rule.decide(1, out, [SKIPPED])

    def test_a_skip_with_the_fields_of_go_test_is_accepted(self) -> None:
        out = failed(never(PKG, "TestRedis"))
        line = ('{"Time":"2026-11-27T20:01:07.6+02:00","Action":"skip","Package":"%s",'
                '"Test":"TestRedis","Elapsed":0,"Output":null,"OutputType":"frame"}\n' % PKG)
        self.assertEqual(rule.decide(1, out, [line]), [(PKG, "TestRedis")])

    def test_a_malformed_record_is_no_evidence(self) -> None:
        # Each line is the only skip of TestRedis, and testbucket's decoder
        # would not read it as an event.
        out = failed(never(PKG, "TestRedis"))
        head = '{"Action":"skip","Package":"%s","Test":"TestRedis"' % PKG
        for line in (
            head + ',"Elapsed":"0.01"}',
            head + ',"Elapsed":NaN}',
            head + ',"Elapsed":0,"Time":NaN}',
            head + ',"Elapsed":Infinity}',
            head + ',"Elapsed":1e400}',
            head + ',"Elapsed":true}',
            head + ',"Elapsed":1e12}',
            '{"Action":1,"Package":"%s","Test":"TestRedis"}' % PKG,
            '{"Action":"skip","Package":null,"Test":"TestRedis"}',
            '{"action":"skip","Package":"%s","Test":"TestRedis"}' % PKG,
            '{"Action":"pass","Action":"skip","Package":"%s","Test":"TestRedis"}' % PKG,
            head + '} trailing',
        ):
            with self.subTest(line=line), self.assertRaisesRegex(rule.RuleError, "not an event that testbucket reads"):
                rule.decide(1, out, [line + "\n"])

    def test_events_that_are_not_json_fail(self) -> None:
        out = failed(never(PKG, "TestRedis"))
        for bad in (SKIPPED + "ok  \texample.com/m/cmd/tool\t0.1s\n", SKIPPED + "[1, 2]\n"):
            with self.subTest(bad=bad), self.assertRaises(rule.RuleError):
                rule.decide(1, out, [bad])


class MainTest(unittest.TestCase):
    """The script as the record job runs it, with a stand-in for testbucket."""

    def run_script(self, audit_output: str, audit_status: int, event_text: str, raw: bytes = b"") -> tuple[int, str, str]:
        with tempfile.TemporaryDirectory() as tmp:
            tmp = pathlib.Path(tmp)
            (tmp / "audit.txt").write_text(audit_output, encoding="utf-8")
            fake = tmp / "testbucket"
            fake.write_text(f'#!/bin/sh\ncat "{tmp / "audit.txt"}"\nexit {audit_status}\n', encoding="utf-8")
            fake.chmod(fake.stat().st_mode | stat.S_IXUSR)
            ev = tmp / "bucket-0-00.ndjson"
            ev.write_bytes(event_text.encode("utf-8") + raw)
            summary = tmp / "summary.md"
            env = dict(os.environ, GITHUB_STEP_SUMMARY=str(summary))
            proc = subprocess.run(
                [sys.executable, str(SCRIPT), "--testbucket", str(fake), "--shard-plan", str(tmp / "plan.json"), str(ev)],
                capture_output=True, text=True, env=env,
            )
            text = summary.read_text(encoding="utf-8") if summary.exists() else ""
            return proc.returncode, proc.stdout + proc.stderr, text

    def test_an_accepted_skip_exits_0_and_is_listed(self) -> None:
        status, out, summary = self.run_script(failed(never(PKG, "TestRedis")), 1, SKIPPED)
        self.assertEqual(status, 0, out)
        self.assertIn(f"accepted, skipped: {PKG}: TestRedis", out)
        self.assertIn(f"accepted, skipped: {PKG}: TestRedis", summary)

    def test_a_missing_test_exits_1(self) -> None:
        status, out, summary = self.run_script(failed(never(PKG, "TestGone")), 1, SKIPPED)
        self.assertEqual(status, 1, out)
        self.assertIn("TestGone", out)
        self.assertEqual(summary, "")

    def test_a_passing_audit_exits_0(self) -> None:
        status, out, summary = self.run_script(PASSED, 0, SKIPPED)
        self.assertEqual(status, 0, out)
        self.assertIn("PASS", out)
        self.assertEqual(summary, "")

    def test_a_passing_audit_reads_no_events(self) -> None:
        # testbucket reads events that hold a byte that is not UTF-8; when its
        # audit passes, the script must not read them again.
        status, out, _ = self.run_script(PASSED, 0, SKIPPED, raw=b"\xff not an event\n")
        self.assertEqual(status, 0, out)


if __name__ == "__main__":
    unittest.main()
