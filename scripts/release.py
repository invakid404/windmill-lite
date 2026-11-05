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
  mode                          print dry_run=true or dry_run=false for the
                                release workflow's run, from its GitHub
                                Actions environment (see release_mode), or
                                exit 1 when the run may not release
  build-engine <out> [--static] build ./cmd/windmill-lite into <out> with
                                the version stamped; --static links it
                                statically, as the Linux release engine
                                is (GOOS, GOARCH, CGO_ENABLED and CC pass
                                through from the environment)
  check-engine <binary>         exit 1 unless `<binary> version` prints
                                "windmill-lite v" + the version
  notices <out>                 write the licence texts that the engine
                                binary must carry into <out>: the
                                Unlicense, the third-party files of this
                                repository that the engine contains, the
                                licences of the Go modules it links, and
                                Go's own

Every command checks the version's form first: MAJOR.MINOR.PATCH with an
optional pre-release, such as 0.1.0 or 0.2.0-rc.1, which npm and Go module
versions both accept. Build metadata (+...) is refused, because Go and
npm treat it differently.
"""

from __future__ import annotations

import json
import os
import pathlib
import re
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
PACKAGE_JSON = REPO / "harnesses" / "package.json"

# A pre-release identifier is a number without leading zeros, or a string
# of letters, digits and hyphens with at least one non-digit (SemVer 2.0.0).
_PRE = r"(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
VERSION_FORM = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    rf"(-{_PRE}(\.{_PRE})*)?"
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


# The repository whose tags release windmill-lite. A fork's runs are dry
# runs, whatever triggers them.
UPSTREAM = "invakid404/windmill-lite"


def release_mode(event: str, ref_type: str, ref_name: str, repository: str, dry_run_input: str, v: str) -> bool:
    """Returns whether a run of the release workflow is a dry run, which
    builds and checks everything and creates and publishes nothing. It
    raises ReleaseError for a run that asks to release where it may not.

    - A pushed tag releases in UPSTREAM, and is a dry run in a fork.
    - workflow_dispatch is a dry run unless its dry_run input is "false";
      then it releases only in UPSTREAM and on a tag.
    - A pull request is always a dry run.
    - On a tag, the tag must be "v" + the version, dry run or not.
    """
    if ref_type == "tag":
        check_tag(ref_name, v)
    match event:
        case "push":
            if ref_type != "tag":
                raise ReleaseError(f"a push of the {ref_type} {ref_name} does not release; push the tag {tag(v)}")
            return repository != UPSTREAM
        case "workflow_dispatch":
            if dry_run_input != "false":
                return True
            if repository != UPSTREAM:
                raise ReleaseError(f"a release runs only in {UPSTREAM}, not in {repository}")
            if ref_type != "tag":
                raise ReleaseError(f"a release runs only from its tag {tag(v)}, not from the {ref_type} {ref_name}")
            return False
        case "pull_request":
            return True
    raise ReleaseError(f"the release workflow does not run on {event!r} events")


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


# The third-party files of this repository whose code or data the engine
# binary contains, as the root README lists them, with what each covers.
VENDORED_NOTICES = [
    ("QuickJS-ng (MIT), compiled into the expression engine",
     ["internal/expr/quickjs/quickjs-ng/LICENSE", "internal/expr/quickjs/quickjs-ng/NOTICE"]),
    ("mimalloc (MIT), the expression engine's allocator",
     ["internal/expr/quickjs/mimalloc/LICENSE", "internal/expr/quickjs/mimalloc/NOTICE"]),
    ("PostgreSQL (PostgreSQL License), ported in internal/jsonval/pglz.go and internal/jsonval/jsonb.go",
     ["internal/jsonval/LICENSE.PostgreSQL"]),
    ("Windmill's OpenAPI description (Apache-2.0), from which the embedded route table internal/platform/facade/routes.json is generated",
     ["internal/oracle/openapi/NOTICE", "internal/oracle/openapi/LICENSE-APACHE"]),
    ("Windmill's OpenFlow schema (Apache-2.0), vendored in internal/openflow/schema",
     ["internal/openflow/schema/NOTICE", "internal/openflow/schema/LICENSE-APACHE"]),
]

_LICENSE_FILE = re.compile(r"(?i)(licen[cs]e|copying|notice|patents)([.-].*)?")


def _section(title: str, text: str) -> str:
    return f"{'=' * 72}\n{title}\n{'=' * 72}\n\n{text.rstrip()}\n\n"


def go_modules() -> list[tuple[str, str, str]]:
    """Returns the Go modules that ./cmd/windmill-lite links, other than
    this one, as (path, version, directory)."""
    res = subprocess.run(
        ["go", "list", "-deps", "-f",
         "{{if and .Module (not .Module.Main)}}{{.Module.Path}}\t{{.Module.Version}}\t{{.Module.Dir}}{{end}}",
         "./cmd/windmill-lite"],
        cwd=REPO, capture_output=True, text=True,
    )
    if res.returncode != 0:
        raise ReleaseError(f"go list failed: {res.stderr.strip()}")
    return sorted({tuple(line.split("\t")) for line in res.stdout.splitlines() if line})


def notices() -> str:
    """Returns the licence texts that a binary of the engine must carry."""
    out = [_section("windmill-lite (Unlicense)", (REPO / "LICENSE").read_text(encoding="utf-8"))]
    for title, files in VENDORED_NOTICES:
        text = "\n\n".join(f"--- {f} ---\n\n{(REPO / f).read_text(encoding='utf-8')}" for f in files)
        out.append(_section(title, text))
    for path, ver, directory in go_modules():
        found = sorted(p for p in pathlib.Path(directory).iterdir() if p.is_file() and _LICENSE_FILE.fullmatch(p.name))
        if not found:
            raise ReleaseError(f"Go module {path} {ver} has no licence file in {directory}")
        text = "\n\n".join(f"--- {p.name} ---\n\n{p.read_text(encoding='utf-8')}" for p in found)
        out.append(_section(f"Go module {path} {ver}", text))
    goroot = pathlib.Path(subprocess.run(["go", "env", "GOROOT"], capture_output=True, text=True, check=True).stdout.strip())
    # Go's own distribution keeps LICENSE and PATENTS in GOROOT; a package
    # manager such as Homebrew may move them one level up.
    texts = []
    for name in ("LICENSE", "PATENTS"):
        found = [d / name for d in (goroot, goroot.parent) if (d / name).is_file()]
        if found:
            texts.append(f"--- {name} ---\n\n{found[0].read_text(encoding='utf-8')}")
        elif name == "LICENSE":
            raise ReleaseError(f"no LICENSE of Go in {goroot} or its parent")
    out.append(_section("Go (the standard library and runtime)", "\n\n".join(texts)))
    return "".join(out)


USAGE = """usage: release.py version
       release.py check-tag <tag>
       release.py mode
       release.py build-engine <out> [--static]
       release.py check-engine <binary>
       release.py notices <out>"""


def main(argv: list[str]) -> int:
    try:
        v = version()
        match argv:
            case ["version"]:
                print(v)
            case ["check-tag", given]:
                check_tag(given, v)
            case ["mode"]:
                env = os.environ
                dry = release_mode(
                    env.get("GITHUB_EVENT_NAME", ""), env.get("GITHUB_REF_TYPE", ""),
                    env.get("GITHUB_REF_NAME", ""), env.get("GITHUB_REPOSITORY", ""),
                    env.get("DRY_RUN_INPUT", ""), v,
                )
                print(f"dry_run={'true' if dry else 'false'}")
            case ["build-engine", out]:
                build_engine(out, False, v)
            case ["build-engine", out, "--static"]:
                build_engine(out, True, v)
            case ["check-engine", binary]:
                check_engine(binary, v)
            case ["notices", out]:
                pathlib.Path(out).write_text(notices(), encoding="utf-8")
            case _:
                print(USAGE, file=sys.stderr)
                return 2
    except (ReleaseError, OSError, ValueError) as e:
        print(f"release.py: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
