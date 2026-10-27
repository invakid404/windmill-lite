#!/usr/bin/env python3
"""Exit 0 iff a go.mod diff is safe to auto-ack.

Safe means: every difference between BASE and HEAD is a version literal
on an existing `require` entry (same module path, same `// indirect`
marker) or on the `toolchain` line. Anything else — a requirement added
or removed, a `go` language-version change, a `replace`, `exclude`,
`retract` or `module` edit, or any reordering — exits non-zero, and the
caller MUST fall back to a human ack.

The check parses both revisions into directive entries rather than
diffing text, so whitespace churn cannot hide a change and cannot cause
a false rejection either. Comments other than `// indirect` are ignored.

Usage: check-go-mod-diff.py <base-ref> [go.mod-path]
"""

from __future__ import annotations

import subprocess
import sys

# A requirement entry is (directive, module path, version, indirect).
# Every other directive is (directive, *tokens).
Entry = tuple[str, ...]


def load_at(ref: str, path: str) -> str:
    result = subprocess.run(
        ["git", "show", f"{ref}:{path}"],
        check=True,
        capture_output=True,
    )
    return result.stdout.decode("utf-8")


def split_comment(line: str) -> tuple[str, str]:
    code, sep, comment = line.partition("//")
    return code.strip(), comment.strip() if sep else ""


def parse(text: str) -> list[Entry]:
    entries: list[Entry] = []
    block: str | None = None
    for raw in text.splitlines():
        code, comment = split_comment(raw)
        if not code:
            continue
        tokens = code.split()
        if block is not None:
            if tokens == [")"]:
                block = None
                continue
            entries.append(entry(block, tokens, comment))
            continue
        if len(tokens) == 2 and tokens[1] == "(":
            block = tokens[0]
            continue
        entries.append(entry(tokens[0], tokens[1:], comment))
    if block is not None:
        raise ValueError(f"unterminated {block} block")
    return entries


def entry(directive: str, tokens: list[str], comment: str) -> Entry:
    if directive == "require":
        if len(tokens) != 2:
            raise ValueError(f"malformed require entry: {tokens}")
        return ("require", tokens[0], tokens[1], "indirect" if comment == "indirect" else "")
    return (directive, *tokens)


def is_version_bump(base: Entry, head: Entry) -> bool:
    if base[0] != head[0]:
        return False
    if base[0] == "require":
        # Same module, same indirect marker; only the version may differ.
        return base[1] == head[1] and base[3] == head[3]
    if base[0] == "toolchain":
        return len(base) == len(head) == 2
    return False


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 3):
        print("usage: check-go-mod-diff.py <base-ref> [go.mod-path]", file=sys.stderr)
        return 2
    base_ref = argv[1]
    path = argv[2] if len(argv) == 3 else "go.mod"

    try:
        base = parse(load_at(base_ref, path))
        head = parse(load_at("HEAD", path))
    except subprocess.CalledProcessError as e:
        stderr = e.stderr.decode(errors="replace") if e.stderr else ""
        print(f"error: cannot read {path} at {base_ref} or HEAD: {stderr}", file=sys.stderr)
        return 3
    except ValueError as e:
        print(f"error: cannot parse {path}: {e}", file=sys.stderr)
        return 3

    if len(base) != len(head):
        print(
            f"UNSAFE: {path} entry count changed ({len(base)} -> {len(head)})",
            file=sys.stderr,
        )
        return 1
    for b, h in zip(base, head):
        if b != h and not is_version_bump(b, h):
            print(f"UNSAFE: non-version change in {path}:", file=sys.stderr)
            print(f"  before: {' '.join(b)}", file=sys.stderr)
            print(f"  after:  {' '.join(h)}", file=sys.stderr)
            return 1

    print(f"SAFE: {path} diff is pure version-literal bumps")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
