#!/usr/bin/env python3
"""The one version of windmill-lite, and the release build of the engine.

The version is the "version" field of harnesses/package.json, the manifest
of the npm package windmill-lite. The engine and the package share it:

- the release build of the engine (build-engine) sets main.version to
  "v" + the version, so `windmill-lite version` prints it;
- the package's build (harnesses/npm/build.ts) stamps the same "v" +
  version into the Bun host SDK, whose handshake then pairs only with an
  engine of that version.

A release is the tag "v" + the version. The release workflow
(.github/workflows/release.yml) and the packaging test
(harnesses/npm/test/check.sh) use these commands:

  version                       print the version
  check-tag <tag>               exit 1 unless <tag> is "v" + the version
  build-engine <out> [--static] build ./cmd/windmill-lite into <out> with
                                the version stamped; --static links it
                                statically, as the Linux release engine
                                is (GOOS, GOARCH, CGO_ENABLED and CC pass
                                through from the environment)
  check-engine <binary>         exit 1 unless `<binary> version` prints
                                "windmill-lite v" + the version

Every command checks the version's form first: MAJOR.MINOR.PATCH with an
optional pre-release, such as 0.1.0 or 0.2.0-rc.1, which npm and Go module
versions both accept. Build metadata (+...) is refused, because Go and
npm treat it differently.
"""

from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
PACKAGE_JSON = REPO / "harnesses" / "package.json"

VERSION_FORM = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?"
)

# The static link of the Linux release engine, as CI's static-linux job
# builds it with zig cc against musl.
STATIC_LDFLAGS = "-linkmode=external -extldflags=-static"


class ReleaseError(Exception):
    pass


def check_form(version: object) -> str:
    """Returns version if it is a release version, and raises otherwise."""
    if not isinstance(version, str) or not VERSION_FORM.fullmatch(version):
        raise ReleaseError(f"version {version!r} is not MAJOR.MINOR.PATCH with an optional pre-release")
    return version


def version(package_json: pathlib.Path = PACKAGE_JSON) -> str:
    """Returns the version in package_json, checked."""
    with open(package_json, encoding="utf-8") as f:
        declared = json.load(f).get("version")
    try:
        return check_form(declared)
    except ReleaseError as e:
        raise ReleaseError(f"{package_json}: {e}") from None


def tag(v: str) -> str:
    """Returns the release tag of version v, which is also the engine's
    version string."""
    return "v" + v


def check_tag(given: str, v: str) -> None:
    if given != tag(v):
        raise ReleaseError(f"tag {given} is not the package version's tag {tag(v)}")


def build_engine(out: str, static: bool, v: str) -> None:
    ldflags = f"-X main.version={tag(v)}"
    if static:
        ldflags += " " + STATIC_LDFLAGS
    cmd = ["go", "build", "-o", out, "-ldflags", ldflags, "./cmd/windmill-lite"]
    print("+ " + " ".join(cmd), file=sys.stderr)
    if subprocess.run(cmd, cwd=REPO).returncode != 0:
        raise ReleaseError("go build failed")


def engine_line(v: str) -> str:
    """Returns what `windmill-lite version` prints for a release engine of
    version v."""
    return f"windmill-lite {tag(v)}\n"


def check_engine(binary: str, v: str) -> None:
    res = subprocess.run([binary, "version"], capture_output=True, text=True)
    if res.returncode != 0 or res.stdout != engine_line(v):
        raise ReleaseError(
            f"{binary} version printed {res.stdout!r} (exit {res.returncode}), "
            f"want {engine_line(v)!r}"
        )


USAGE = """usage: release.py version
       release.py check-tag <tag>
       release.py build-engine <out> [--static]
       release.py check-engine <binary>"""


def main(argv: list[str]) -> int:
    try:
        v = version()
        match argv:
            case ["version"]:
                print(v)
            case ["check-tag", given]:
                check_tag(given, v)
            case ["build-engine", out]:
                build_engine(out, False, v)
            case ["build-engine", out, "--static"]:
                build_engine(out, True, v)
            case ["check-engine", binary]:
                check_engine(binary, v)
            case _:
                print(USAGE, file=sys.stderr)
                return 2
    except (ReleaseError, OSError, ValueError) as e:
        print(f"release.py: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
