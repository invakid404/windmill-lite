#!/usr/bin/env python3
"""Report Markdown that breaks the countable rules of docs/writing.md.

The rules:

- paragraph-length: a block of prose (a paragraph, a list item or a table
  cell) has more than MAX_SENTENCES sentences or more than MAX_WORDS words.
- sentence-length: a sentence has more than MAX_SENTENCE_WORDS words.
- current-state: a README.md has no `**Current state:**` line before its
  first `##` heading.
- work-code: a block or a heading names an internal plan or work code: a
  milestone ID such as F3b, a plan item such as M3-F5, a wave number, a
  review or fix round, or a pull request number.

The check is advisory. It prints each finding and exits 0, so CI reports
findings without failing; --strict exits 1 when there is a finding.

Code blocks, inline code, HTML comments (Memoria markers among them) and
link targets are not prose. Inline code counts as one word, so a work code
in inline code is not reported. A list item counts as one block with all
its paragraphs. Headings are checked for work codes only. Sentence counts
are a heuristic (see SENTENCE_END).

Usage: check-docs.py [--diff-base REV] [--github] [--summary FILE] [--strict] [PATH ...]

Without PATH it checks every Markdown file that Git tracks, except the
skill packages that Memoria installs. --diff-base REV reports only the
blocks that overlap lines changed since REV, in the Markdown files changed
since REV. --github prints GitHub Actions warning annotations. --summary
appends a Markdown summary of the findings to FILE.
"""

from __future__ import annotations

import argparse
import collections
import dataclasses
import re
import subprocess
import sys

# The limits of docs/writing.md ("Structure" and "Sentences").
MAX_SENTENCES = 4
MAX_WORDS = 80
MAX_SENTENCE_WORDS = 40

CURRENT_STATE = "**Current state:**"

# Skill packages that `memoria integrations skill` writes and owns.
SKIP_PREFIXES = (".agents/", ".claude/")

FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
HEADING = re.compile(r"^ {0,3}#{1,6}(?:\s|$)")
THEMATIC_BREAK = re.compile(r"^ {0,3}([-*_])(?:\s*\1){2,}\s*$")
LIST_ITEM = re.compile(r"^\s*(?:[-*+]|\d{1,9}[.)])\s+")
TABLE_ROW = re.compile(r"^ {0,3}\|")
TABLE_DELIMITER = re.compile(r"^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$")
LINK_DEFINITION = re.compile(r"^ {0,3}\[[^\]]+\]:\s")
HTML_BLOCK = re.compile(r"^ {0,3}<(?!(?:https?|mailto):)[A-Za-z/!?]")
QUOTE = re.compile(r"^(?: {0,3}> ?)+")

CODE_SPAN = re.compile(r"(`+)(.+?)\1")
LINK = re.compile(r"!?\[([^\]]*)\](?:\([^)]*\)|\[[^\]]*\])")
AUTOLINK = re.compile(r"<(?:https?|mailto):[^>]+>")
HTML_TAG = re.compile(r"</?[A-Za-z][^>]*>")
# Stands in for an inline code span or an autolink: one word, and a
# possible start of a sentence.
PLACEHOLDER = "@code@"

# A heuristic: a sentence ends at ".", "!" or "?" and a space, except after
# a common abbreviation. The next sentence can start in lower case, as a
# product name such as windmill-lite does.
SENTENCE_END = re.compile(r"[.!?][\"'”’)\]*_]*\s+(?=[A-Za-z0-9\"“'(\[@*_])")
ABBREVIATIONS = {"e.g", "i.e", "etc", "vs", "cf", "approx", "no", "fig", "al", "ca"}
WORD = re.compile(r"\w")

# Milestone IDs use the prefixes of ADR 0012's tables and of the follow-up,
# semantics-revision and documentation milestones. Deviations (D9) and hooks
# (H3) are identifiers that ADRs define, so they are not work codes. A
# defined name that looks like a milestone ID (a fixture called B2) is still
# reported; the reviewer decides.
ORDINAL = r"(?:first|second|third|fourth|fifth|\d+(?:st|nd|rd|th))"
WORK_CODES = (
    re.compile(r"(?<![\w-])(?:(?:[ABCEFGKNRW]|PF|FU|SC|DS)[1-9]\d?[a-z]?|NI)\b(?!-\w)"),
    re.compile(r"\b(?:SR-\d+|M(?:D|\d+)-[A-Z]+\d+)\b"),
    re.compile(r"(?<![\w&/])#\d{1,5}\b"),
    re.compile(rf"\bwaves? \d+\b|\b(?:review |fix )?rounds? \d+\b|\b{ORDINAL} (?:review |fix )?round\b", re.IGNORECASE),
)

HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")


@dataclasses.dataclass(frozen=True)
class Block:
    start: int  # first line, 1-based
    end: int  # last line
    kind: str  # "paragraph", "list item", "table cell" or "heading"
    text: str


@dataclasses.dataclass(frozen=True)
class Finding:
    path: str
    line: int
    rule: str
    message: str


def table_cells(row: str) -> list[str]:
    """Split a table row on unescaped pipes, as GitHub-flavoured Markdown does."""
    text = row.strip()
    if text.startswith("|"):
        text = text[1:]
    if text.endswith("|") and not text.endswith("\\|"):
        text = text[:-1]
    return [cell.strip() for cell in re.split(r"(?<!\\)\|", text)]


def blocks(text: str) -> list[Block]:
    """Return the blocks of prose in a Markdown document."""
    out: list[Block] = []
    lines = text.splitlines()
    first = 0
    if lines and lines[0].strip() == "---":
        for number in range(1, len(lines)):
            if lines[number].strip() in ("---", "..."):
                first = number + 1
                break

    current: list[str] = []
    start = end = 0
    kind = "paragraph"
    fence = ""
    in_comment = False
    in_list = False  # indented text after a list item belongs to that item
    held = False  # a list item stays open across a blank line for its next paragraph

    def flush() -> None:
        nonlocal held
        held = False
        if current:
            out.append(Block(start, end, kind, " ".join(current)))
            current.clear()

    for number, line in enumerate(lines[first:], start=first + 1):
        line = QUOTE.sub("", line)  # a block quote holds Markdown of its own
        stripped = line.strip()
        if fence:
            if re.fullmatch(rf"{re.escape(fence[0])}{{{len(fence)},}}", stripped):
                fence = ""
            continue
        if in_comment:
            in_comment = "-->" not in line
            continue
        indent = len(line) - len(line.lstrip(" "))
        if held and stripped and (indent == 0 or LIST_ITEM.match(line)):
            flush()
        match = FENCE.match(line)
        if match:
            flush()
            fence = match.group(1)
            continue
        if stripped.startswith("<!--"):
            flush()
            in_comment = "-->" not in stripped[4:]
            continue
        if not stripped:
            if current and kind == "list item":
                held = True
            else:
                flush()
            continue
        if HEADING.match(line):
            flush()
            in_list = False
            out.append(Block(number, number, "heading", stripped.lstrip("#").strip()))
            continue
        if THEMATIC_BREAK.match(line) or HTML_BLOCK.match(line):
            flush()
            in_list = False
            continue
        if TABLE_ROW.match(line):
            flush()
            in_list = False
            if not TABLE_DELIMITER.match(line):
                for cell in table_cells(line):
                    if cell:
                        out.append(Block(number, number, "table cell", cell))
            continue
        item = LIST_ITEM.match(line)
        if item:
            flush()
            start = end = number
            kind, in_list = "list item", True
            current.append(line[item.end():].strip())
            continue
        if current:
            current.append(stripped)
            end, held = number, False
            continue
        if LINK_DEFINITION.match(line):
            continue
        if indent >= 4 and not in_list:
            continue  # an indented code block
        start = end = number
        kind = "list item" if in_list and indent > 0 else "paragraph"
        in_list = kind == "list item"
        current.append(stripped)
    flush()
    return out


def prose(text: str) -> str:
    """Strip Markdown syntax that is not prose."""
    text = CODE_SPAN.sub(PLACEHOLDER, text)
    text = AUTOLINK.sub(PLACEHOLDER, text)
    text = LINK.sub(r"\1", text)
    text = HTML_TAG.sub("", text)
    return text


def words(text: str) -> int:
    return sum(1 for token in text.split() if WORD.search(token))


def sentences(text: str) -> list[str]:
    parts: list[str] = []
    last = 0
    for match in SENTENCE_END.finditer(text):
        before = text[last:match.start()].split()
        if before and before[-1].lstrip("(\"'").lower() in ABBREVIATIONS:
            continue
        parts.append(text[last:match.end()].strip())
        last = match.end()
    parts.append(text[last:].strip())
    return [part for part in parts if words(part)]


def opening(sentence: str, count: int = 8) -> str:
    tokens = sentence.split()
    return " ".join(tokens[:count]) + (" …" if len(tokens) > count else "")


def has_current_state(text: str) -> bool:
    fence = False
    in_comment = False
    for line in text.splitlines():
        stripped = line.strip()
        if in_comment:
            in_comment = "-->" not in line
            continue
        if not fence and stripped.startswith("<!--"):
            in_comment = "-->" not in stripped[4:]
            continue
        if FENCE.match(line):
            fence = not fence
            continue
        if fence:
            continue
        if line.startswith("## "):
            return False
        if line.lstrip().startswith(CURRENT_STATE):
            return True
    return False


def work_codes(text: str) -> list[str]:
    found: list[str] = []
    for pattern in WORK_CODES:
        for match in pattern.finditer(text):
            if match.group(0) not in found:
                found.append(match.group(0))
    return found


def check_block(path: str, block: Block) -> list[Finding]:
    findings: list[Finding] = []
    body = prose(block.text)
    codes = work_codes(body)
    if codes:
        findings.append(Finding(
            path, block.start, "work-code",
            f"{block.kind} names {', '.join(codes)}; name the behaviour or the decision instead",
        ))
    if block.kind == "heading":
        return findings
    parts = sentences(body)
    total = words(body)
    if len(parts) > MAX_SENTENCES or total > MAX_WORDS:
        findings.append(Finding(
            path, block.start, "paragraph-length",
            f"{block.kind} of {len(parts)} sentences and {total} words; "
            f"the limit is {MAX_SENTENCES} sentences and {MAX_WORDS} words",
        ))
    for sentence in parts:
        count = words(sentence)
        if count > MAX_SENTENCE_WORDS:
            findings.append(Finding(
                path, block.start, "sentence-length",
                f'sentence of {count} words, over {MAX_SENTENCE_WORDS}: "{opening(sentence)}"',
            ))
    return findings


def check_text(path: str, text: str, changed: list[tuple[int, int]] | None = None) -> list[Finding]:
    """Check one document. With changed, check only blocks that overlap those line ranges."""
    findings: list[Finding] = []
    for block in blocks(text):
        if changed is None or any(block.start <= end and start <= block.end for start, end in changed):
            findings.extend(check_block(path, block))
    if path.rsplit("/", 1)[-1] == "README.md" and not has_current_state(text):
        findings.append(Finding(path, 1, "current-state", f"no {CURRENT_STATE} line before the first ## heading"))
    return findings


def changed_lines(diff: str) -> dict[str, list[tuple[int, int]]]:
    """Map each file in a `git diff -U0` to the line ranges it added or changed."""
    ranges: dict[str, list[tuple[int, int]]] = {}
    path: str | None = None
    for line in diff.splitlines():
        if line.startswith("+++ "):
            target = line[4:]
            path = target[2:] if target.startswith("b/") else None
            if path is not None:
                ranges.setdefault(path, [])
        elif path is not None:
            match = HUNK.match(line)
            if match and match.group(2) != "0":
                start = int(match.group(1))
                ranges[path].append((start, start + int(match.group(2) or "1") - 1))
    return ranges


def git(*args: str) -> str:
    result = subprocess.run(["git", "-c", "core.quotePath=false", *args], check=True, capture_output=True)
    return result.stdout.decode("utf-8")


def tracked_markdown() -> list[str]:
    return [path for path in git("ls-files", "-z", "--", "*.md").split("\0") if path]


def escape_data(text: str) -> str:
    return text.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")


def escape_property(text: str) -> str:
    return escape_data(text).replace(":", "%3A").replace(",", "%2C")


def annotation(finding: Finding) -> str:
    return (
        f"::warning file={escape_property(finding.path)},line={finding.line},"
        f"title={escape_property('docs ' + finding.rule)}::{escape_data(finding.message)}"
    )


def summary(findings: list[Finding], files: int) -> str:
    lines = [
        "## Documentation check (report only)",
        "",
        f"{len(findings)} warnings in {len({f.path for f in findings})} of {files} Markdown files, "
        "against the countable rules of docs/writing.md (What CI checks).",
    ]
    if findings:
        lines += ["", "| Rule | Warnings |", "|---|---:|"]
        for rule, count in sorted(collections.Counter(f.rule for f in findings).items()):
            lines.append(f"| `{rule}` | {count} |")
        lines += ["", "| File | Warnings |", "|---|---:|"]
        for path, count in collections.Counter(f.path for f in findings).most_common(15):
            lines.append(f"| `{path}` | {count} |")
    return "\n".join(lines) + "\n"


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("paths", nargs="*", metavar="PATH")
    parser.add_argument("--diff-base", metavar="REV")
    parser.add_argument("--github", action="store_true")
    parser.add_argument("--summary", metavar="FILE")
    parser.add_argument("--strict", action="store_true")
    args = parser.parse_args(argv)

    paths = args.paths or tracked_markdown()
    changed: dict[str, list[tuple[int, int]]] | None = None
    if args.diff_base:
        changed = changed_lines(git(
            "diff", "--no-color", "--no-ext-diff", "--no-renames", "-U0",
            "--diff-filter=AM", args.diff_base, "--", "*.md",
        ))
        paths = [path for path in paths if path in changed]
    paths = [path for path in paths if not path.startswith(SKIP_PREFIXES)]

    findings: list[Finding] = []
    for path in paths:
        with open(path, encoding="utf-8") as file:
            text = file.read()
        findings.extend(check_text(path, text, None if changed is None else changed[path]))

    for finding in findings:
        print(annotation(finding) if args.github else f"{finding.path}:{finding.line}: {finding.rule}: {finding.message}")
    if args.summary:
        with open(args.summary, "a", encoding="utf-8") as file:
            file.write(summary(findings, len(paths)))
    return 1 if args.strict and findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
