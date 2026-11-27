#!/usr/bin/env python3
"""Audit a bucketed Go test run, counting a skipped top-level test as run.

The record jobs of the CI workflow run this instead of `testbucket audit`
alone. It runs `testbucket audit` first, unchanged, and prints its output.
When the audit passes, so does this script.

testbucket v0.2.2's audit counts a top-level test of a sliced package as
reported only when it passes. A test that skips, such as one that needs a
Redis server that the buckets do not have, therefore fails the audit as
"<package>: <test> was in a -run slice but never reported". When the audit
fails, this script exits 0 only if every discrepancy is of that form and the
events hold a top-level `skip` of that test in that package. It prints each
test that it accepts, and appends them to the GitHub step summary.

Anything else exits 1: another kind of discrepancy, such as a package that
reported nothing, a test that reported nothing and did not skip, an output
line of the audit that this script does not know, or a line of the events
that testbucket would not read as an event. The script reads the events
only when the audit failed.

Usage: check-bucket-audit.py --shard-plan <shard-plan.json> [--testbucket <path>] <events.ndjson>...
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys

# The lines of a failed audit of testbucket v0.2.2 (internal/core/audit.go):
# the report on standard output, then the error that the command prints on
# standard error.
REPORT_LINES = (
    re.compile(r"^testbucket audit — \d+ planned unit\(s\) over \d+ package\(s\)$"),
    re.compile(r"^  packages that reported a result +\d+$"),
    re.compile(r"^  run-sliced packages name-checked +\d+$"),
)
FAILED_LINE = "testbucket: coverage audit FAILED: the run did not execute what the plan scheduled"
SLICE_GROUP = "  -run slice discrepancies:"
OTHER_GROUPS = (
    "  package(s) that never reported — a bucket produced no events:",
    "  package(s) that reported fewer invocations than planned — a shard or slice is missing:",
    "  package(s) that reported more invocations than planned:",
    "  package(s) that ran but were in no bucket:",
)
TRAILER_LINES = (
    "The plan's coverage gate proves the MATRIX is complete before anything",
    "runs. This proves the RUN was. A bucket that produced no events is",
    "invisible to the gate and is exactly what this catches.",
)
NEVER_REPORTED = re.compile(r"^    - (\S+): (\S+) was in a -run slice but never reported$")

# The fields of a `go test -json` event that testbucket v0.2.2 decodes
# (internal/runner/gorunner/ingest.go, testEvent), and the bound beyond which
# it drops an event's Elapsed as implausible (internal/runner/summary.go).
EVENT_FIELDS = {"action": "Action", "package": "Package", "test": "Test", "elapsed": "Elapsed"}
MAX_PLAUSIBLE_SECONDS = 1 << 30


class RuleError(Exception):
    """The run does not pass the audit, with the reason."""


def never_reported(output: str) -> list[tuple[str, str]]:
    """Returns the (package, test) pairs of a failed audit's output.

    It raises RuleError for a discrepancy of another kind, and for any line
    that is not part of a failed audit's output.
    """
    pairs: list[tuple[str, str]] = []
    failed = False
    group = None
    for line in output.splitlines():
        if line == "" or line in TRAILER_LINES or any(p.match(line) for p in REPORT_LINES):
            group = None
            continue
        if line == FAILED_LINE:
            failed = True
            continue
        if line == SLICE_GROUP:
            group = SLICE_GROUP
            continue
        if line in OTHER_GROUPS:
            raise RuleError(f"the audit found a discrepancy that is not a skipped test: {line.strip()}")
        if group == SLICE_GROUP and line.startswith("    - "):
            m = NEVER_REPORTED.match(line)
            if m is None:
                raise RuleError(f"the audit found a discrepancy that is not a skipped test: {line.strip()}")
            pkg, name = m.groups()
            if "/" in name:
                raise RuleError(f"not a top-level test: {pkg}: {name}")
            pairs.append((pkg, name))
            continue
        raise RuleError(f"an output line of the audit that this script does not know: {line!r}")
    if not failed:
        raise RuleError("the audit failed without its coverage report")
    if not pairs:
        raise RuleError("the audit failed, but named no test")
    return pairs


def _no_constant(name: str) -> float:
    raise ValueError(f"{name} is not JSON")


def event(line: str) -> dict[str, object]:
    """Decodes one line of `go test -json` output as testbucket does.

    It returns the fields that testbucket reads. It raises ValueError for a
    line that testbucket would not read as an event: not a JSON object, a
    field of the wrong type, NaN or Infinity, an Elapsed beyond testbucket's
    bound, and also a field name in another letter case or given twice,
    which go test never writes.
    """
    if not line.startswith("{"):
        raise ValueError("not a JSON object")
    pairs = json.loads(line, object_pairs_hook=list, parse_constant=_no_constant)
    if not isinstance(pairs, list):
        raise ValueError("not a JSON object")
    fields: dict[str, object] = {}
    for key, value in pairs:
        name = EVENT_FIELDS.get(key.casefold())
        if name is None:
            continue
        if key != name or name in fields:
            raise ValueError(f"field {key!r}")
        if name == "Elapsed":
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                raise ValueError(f"{name} is not a number")
            seconds = float(value)
            # NaN and the infinities fail the comparison too.
            if seconds != 0 and not -MAX_PLAUSIBLE_SECONDS <= seconds <= MAX_PLAUSIBLE_SECONDS:
                raise ValueError(f"{name} {value} is not a plausible duration")
        elif not isinstance(value, str):
            raise ValueError(f"{name} is not a string")
        fields[name] = value
    return fields


def skips(texts: list[str]) -> set[tuple[str, str]]:
    """Returns the (package, test) pairs of the tests' `skip` events.

    A subtest's name holds a `/`, and never_reported refuses such a name, so
    only a top-level test's skip can match. It raises RuleError for any
    non-empty line that event refuses.
    """
    found: set[tuple[str, str]] = set()
    for text in texts:
        for number, line in enumerate(text.splitlines(), 1):
            line = line.strip()
            if not line:
                continue
            try:
                fields = event(line)
            except (ValueError, OverflowError) as err:
                raise RuleError(f"line {number} of the events is not an event that testbucket reads: {err}") from None
            test = fields.get("Test") or ""
            if fields.get("Action") == "skip" and test:
                found.add((fields.get("Package") or "", test))
    return found


def decide(audit_status: int, audit_output: str, events: list[str]) -> list[tuple[str, str]]:
    """Returns the skipped tests that the rule accepts; [] when the audit passed.

    It raises RuleError when the run does not pass.
    """
    if audit_status == 0:
        return []
    pairs = never_reported(audit_output)
    skipped = skips(events)
    missing = [f"{pkg}: {name}" for pkg, name in pairs if (pkg, name) not in skipped]
    if missing:
        raise RuleError("never reported, and did not skip: " + ", ".join(missing))
    return pairs


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--shard-plan", required=True)
    parser.add_argument("--testbucket", default="testbucket")
    parser.add_argument("events", nargs="+")
    args = parser.parse_args(argv)

    audit = subprocess.run(
        [args.testbucket, "audit", "--runner", "go", "--shard-plan", args.shard_plan, *args.events],
        capture_output=True,
        text=True,
    )
    output = audit.stdout + audit.stderr
    sys.stdout.write(output)
    sys.stdout.flush()
    if audit.returncode == 0:
        return 0
    try:
        events = []
        for path in args.events:
            with open(path, encoding="utf-8") as f:
                events.append(f.read())
        accepted = decide(audit.returncode, output, events)
    except (RuleError, OSError, UnicodeDecodeError) as err:
        print(f"check-bucket-audit: the run fails the audit: {err}", file=sys.stderr)
        return 1
    lines = [f"accepted, skipped: {pkg}: {name}" for pkg, name in accepted]
    print("check-bucket-audit: the audit's only discrepancies are top-level tests that skipped:")
    for line in lines:
        print(f"  {line}")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as f:
            f.write("Top-level tests that skipped, which the audit counts as never reported:\n\n")
            for line in lines:
                f.write(f"- {line}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
