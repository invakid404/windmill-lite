"""Tests for check-docs.py. Run: python3 -m unittest discover -s scripts"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import unittest

_spec = importlib.util.spec_from_file_location(
    "check_docs", pathlib.Path(__file__).with_name("check-docs.py")
)
checker = importlib.util.module_from_spec(_spec)
# dataclasses looks the module up by name while it builds the classes.
sys.modules[_spec.name] = checker
_spec.loader.exec_module(checker)

REPO = pathlib.Path(__file__).resolve().parent.parent


def words(count: int) -> str:
    """A sentence of exactly count words."""
    return " ".join(["Start"] + ["word"] * (count - 2) + ["end."])


def rules(path: str, text: str) -> list[str]:
    return [finding.rule for finding in checker.check_text(path, text)]


class LengthTest(unittest.TestCase):
    def test_paragraph_at_the_limits_passes(self) -> None:
        paragraph = " ".join([words(20)] * 4)  # 4 sentences, 80 words
        self.assertEqual(rules("doc.md", paragraph), [])

    def test_paragraph_over_the_word_limit(self) -> None:
        paragraph = " ".join([words(27)] * 3)  # 3 sentences, 81 words
        self.assertEqual(rules("doc.md", paragraph), ["paragraph-length"])

    def test_paragraph_over_the_sentence_limit(self) -> None:
        paragraph = " ".join(["One idea here."] * 5)
        self.assertEqual(rules("doc.md", paragraph), ["paragraph-length"])

    def test_sentence_over_the_limit(self) -> None:
        self.assertEqual(rules("doc.md", words(40)), [])
        findings = checker.check_text("doc.md", "Intro.\n" + words(41))
        self.assertEqual([f.rule for f in findings], ["sentence-length"])
        self.assertIn("41 words", findings[0].message)
        self.assertEqual(findings[0].line, 1)

    def test_a_long_block_reports_both_rules(self) -> None:
        self.assertEqual(rules("doc.md", words(90)), ["paragraph-length", "sentence-length"])


class BlockTest(unittest.TestCase):
    def test_blank_lines_separate_paragraphs(self) -> None:
        text = f"{words(50)}\n\n{words(40)}\n"
        self.assertEqual(rules("doc.md", text), ["sentence-length"])
        self.assertEqual([b.start for b in checker.blocks(text)], [1, 3])

    def test_wrapped_lines_join_one_paragraph(self) -> None:
        text = "\n".join([words(30)] * 3)
        self.assertEqual(rules("doc.md", text), ["paragraph-length"])

    def test_each_list_item_is_a_block(self) -> None:
        text = "\n".join(f"- {words(30)}" for _ in range(3))
        self.assertEqual(rules("doc.md", text), [])
        self.assertEqual([b.kind for b in checker.blocks(text)], ["list item"] * 3)

    def test_list_item_continuation_lines_belong_to_the_item(self) -> None:
        text = f"1. {words(45)}\n   {words(45)}\n"
        blocks = checker.blocks(text)
        self.assertEqual([(b.kind, b.start, b.end) for b in blocks], [("list item", 1, 2)])

    def test_a_list_item_counts_with_all_its_paragraphs(self) -> None:
        text = "- One. Two. Three.\n\n  Four. Five.\n\nNext paragraph.\n"
        blocks = checker.blocks(text)
        self.assertEqual([(b.kind, b.start, b.end) for b in blocks], [("list item", 1, 3), ("paragraph", 5, 5)])
        self.assertEqual(rules("doc.md", text), ["paragraph-length"])

    def test_a_new_item_or_unindented_text_ends_a_list_item(self) -> None:
        text = "- One.\n\n- Two.\n\nThree.\n"
        self.assertEqual([(b.kind, b.start) for b in checker.blocks(text)],
                         [("list item", 1), ("list item", 3), ("paragraph", 5)])

    def test_each_table_cell_is_a_block(self) -> None:
        text = f"| Name | Meaning |\n|---|---:|\n| a | {words(30)} |\n| b | {words(85)} |\n"
        findings = checker.check_text("doc.md", text)
        self.assertEqual([(f.rule, f.line) for f in findings], [("paragraph-length", 4), ("sentence-length", 4)])

    def test_escaped_pipe_stays_in_its_cell(self) -> None:
        self.assertEqual(checker.table_cells(r"| a \| b | c |"), [r"a \| b", "c"])

    def test_code_and_comments_are_not_prose(self) -> None:
        long = words(90)
        text = "\n".join([
            "```sh",
            long,
            "```",
            "~~~~",
            long,
            "~~~~",
            "<!-- memoria:section id=\"x\" files=\"a.go\" -->",
            "<!--",
            long,
            "-->",
            f"<details><summary>{long}</summary>",
            "---",
            f"    {long}",
        ])
        self.assertEqual(checker.blocks(text), [])

    def test_headings_are_checked_for_work_codes_only(self) -> None:
        text = f"# {words(90)}\n\n## Added in F3b\n"
        self.assertEqual([b.kind for b in checker.blocks(text)], ["heading", "heading"])
        self.assertEqual([(f.rule, f.line) for f in checker.check_text("doc.md", text)], [("work-code", 3)])

    def test_indented_text_after_a_list_item_is_prose(self) -> None:
        text = f"- Item.\n\n    {words(45)}\n"
        self.assertEqual(rules("doc.md", text), ["sentence-length"])

    def test_block_quotes_hold_markdown(self) -> None:
        text = f"> Quoted.\n>\n> | A | B |\n> |---|---|\n> | {words(85)} | b |\n"
        kinds = [b.kind for b in checker.blocks(text)]
        self.assertEqual(kinds, ["paragraph", "table cell", "table cell", "table cell", "table cell"])

    def test_front_matter_is_not_prose(self) -> None:
        text = f"---\ntitle: {words(90)}\n---\nBody.\n"
        self.assertEqual([b.text for b in checker.blocks(text)], ["Body."])


class SentenceTest(unittest.TestCase):
    def test_inline_code_counts_as_one_word_and_hides_its_dots(self) -> None:
        body = checker.prose("Set `memoria.toml` and `a b c d`. Then run it.")
        self.assertEqual(checker.sentences(body), ["Set @code@ and @code@.", "Then run it."])
        self.assertEqual(checker.words(body), 7)

    def test_a_sentence_can_start_with_inline_code(self) -> None:
        self.assertEqual(len(checker.sentences(checker.prose("Run it. `memoria check` fails."))), 2)

    def test_abbreviations_do_not_end_a_sentence(self) -> None:
        body = "Use a pinned version (e.g. Bun 1.3.10), i.e. One that CI installs."
        self.assertEqual(len(checker.sentences(body)), 1)

    def test_a_sentence_can_start_in_lower_case(self) -> None:
        text = "One. windmill-lite runs. windmill-lite stops. windmill-lite waits. windmill-lite ends."
        self.assertEqual(len(checker.sentences(text)), 5)
        self.assertEqual(rules("doc.md", text), ["paragraph-length"])

    def test_version_numbers_do_not_end_a_sentence(self) -> None:
        self.assertEqual(len(checker.sentences("Windmill v1.767.0 ships Bun 1.3.10 today.")), 1)

    def test_links_count_their_text_only(self) -> None:
        body = checker.prose("Read [the long guide](https://example.com/a/b/c) and <https://example.com>.")
        self.assertEqual(body, "Read the long guide and @code@.")

    def test_punctuation_is_not_a_word(self) -> None:
        self.assertEqual(checker.words("a — b | c"), 3)


class WorkCodeTest(unittest.TestCase):
    def test_milestone_ids_and_plan_codes_are_reported(self) -> None:
        for code in ("F3b", "SC5", "DS1", "N5", "B1a", "PF2", "FU8", "NI", "SR-4", "M3-F5", "MD-D9", "R16"):
            with self.subTest(code=code):
                self.assertEqual(checker.work_codes(f"Added in {code} last week."), [code])

    def test_process_words_and_pull_request_numbers_are_reported(self) -> None:
        self.assertEqual(checker.work_codes("Wave 2 lands it (#77)."), ["#77", "Wave 2"])
        self.assertEqual(checker.work_codes("The second review round fixed it."), ["second review round"])
        self.assertEqual(checker.work_codes("Fix round 2 and round 3 did."), ["Fix round 2", "round 3"])
        self.assertEqual(checker.work_codes("Keep review rounds out of the tree."), [])

    def test_identifiers_that_documents_define_are_not_work_codes(self) -> None:
        text = "WR-2, OR-30, FX-40, Q-WA-4, deviation D9, hook H3, V8, ADR 0006, S3, E2E, be664ca1, 1.3.10, C0, F7-seq"
        self.assertEqual(checker.work_codes(text), [])

    def test_one_finding_for_each_block(self) -> None:
        findings = checker.check_text("doc.md", "F3b and F3b, then SC5.\n")
        self.assertEqual([(f.rule, f.message) for f in findings], [
            ("work-code", "paragraph names F3b, SC5; name the behaviour or the decision instead"),
        ])

    def test_a_work_code_in_inline_code_is_not_reported(self) -> None:
        # A known limit, which docs/writing.md states: inline code is not prose.
        self.assertEqual(checker.check_text("doc.md", "Added in `F3b`.\n"), [])

    def test_inline_code_and_link_targets_are_not_checked(self) -> None:
        text = "Run `wltest record F3b` from [the oracle guide](../x.md#batch-e3f).\n"
        self.assertEqual(checker.check_text("doc.md", text), [])


class CurrentStateTest(unittest.TestCase):
    def test_readme_needs_a_current_state_line_before_the_first_section(self) -> None:
        good = "# pkg\n\n**Current state:** works.\n\n## Use\n"
        late = "# pkg\n\nIntro.\n\n## Use\n\n**Current state:** works.\n"
        self.assertEqual(rules("pkg/README.md", good), [])
        self.assertEqual(rules("pkg/README.md", late), ["current-state"])
        self.assertEqual(rules("README.md", "# x\n"), ["current-state"])

    def test_other_documents_need_none(self) -> None:
        self.assertEqual(rules("docs/guide.md", "# Guide\n\nText.\n"), [])

    def test_a_line_in_an_html_comment_does_not_count(self) -> None:
        text = "<!--\n**Current state:** works.\n-->\n# Package\n"
        self.assertEqual(rules("pkg/README.md", text), ["current-state"])
        self.assertEqual(rules("pkg/README.md", "<!-- x -->\n**Current state:** works.\n"), [])

    def test_a_line_in_a_code_block_does_not_count(self) -> None:
        text = "# pkg\n\n```\n**Current state:** example\n```\n"
        self.assertEqual(rules("pkg/README.md", text), ["current-state"])


class DiffTest(unittest.TestCase):
    DIFF = "\n".join([
        "diff --git a/a.md b/a.md",
        "--- a/a.md",
        "+++ b/a.md",
        "@@ -3 +3,2 @@",
        "@@ -9,0 +11 @@",
        "@@ -20,2 +22,0 @@",
        "diff --git a/new.md b/new.md",
        "--- /dev/null",
        "+++ b/new.md",
        "@@ -0,0 +1,4 @@",
    ])

    def test_changed_lines_reads_added_and_changed_ranges(self) -> None:
        self.assertEqual(checker.changed_lines(self.DIFF), {"a.md": [(3, 4), (11, 11)], "new.md": [(1, 4)]})

    def test_only_blocks_that_overlap_a_change_are_checked(self) -> None:
        text = f"{words(50)}\n\n{words(50)}\n{words(10)}\n"  # blocks at lines 1 and 3-4
        self.assertEqual([f.line for f in checker.check_text("doc.md", text, [(4, 4)])], [3])
        self.assertEqual(checker.check_text("doc.md", text, []), [])

    def test_a_changed_readme_is_checked_for_its_current_state_line(self) -> None:
        self.assertEqual([f.rule for f in checker.check_text("README.md", "# x\n", [])], ["current-state"])


class OutputTest(unittest.TestCase):
    def test_annotation_escapes_properties_and_data(self) -> None:
        finding = checker.Finding("a,b:c.md", 7, "sentence-length", "50% of it\nis long")
        self.assertEqual(
            checker.annotation(finding),
            "::warning file=a%2Cb%3Ac.md,line=7,title=docs sentence-length::50%25 of it%0Ais long",
        )

    def test_summary_counts_rules_and_files(self) -> None:
        findings = [
            checker.Finding("a.md", 1, "paragraph-length", ""),
            checker.Finding("a.md", 2, "sentence-length", ""),
            checker.Finding("b.md", 1, "sentence-length", ""),
        ]
        text = checker.summary(findings, 5)
        self.assertIn("3 warnings in 2 of 5 Markdown files", text)
        self.assertIn("| `sentence-length` | 2 |", text)
        self.assertIn("| `a.md` | 2 |", text)


class MainTest(unittest.TestCase):
    """Runs main() in a temporary Git repository."""

    def setUp(self) -> None:
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.cwd = os.getcwd()
        self.addCleanup(os.chdir, self.cwd)
        os.chdir(self.dir.name)
        self.git("init", "-q")
        os.makedirs(".claude/skills")
        pathlib.Path(".claude/skills/SKILL.md").write_text(words(90) + "\n")
        pathlib.Path("doc.md").write_text(f"Short.\n\n{words(50)}\n")
        self.git("add", ".")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")

    def git(self, *args: str) -> None:
        subprocess.run(["git", *args], check=True, capture_output=True)

    def run_main(self, *args: str) -> tuple[int, str]:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = checker.main(list(args))
        return code, out.getvalue()

    def test_findings_never_fail_without_strict(self) -> None:
        code, out = self.run_main()
        self.assertEqual(code, 0)
        self.assertEqual(out, 'doc.md:3: sentence-length: sentence of 50 words, over 40: "Start word word word word word word word …"\n')

    def test_strict_fails_on_a_finding(self) -> None:
        self.assertEqual(self.run_main("--strict")[0], 1)
        pathlib.Path("doc.md").write_text("Short.\n")
        self.assertEqual(self.run_main("--strict"), (0, ""))

    def test_diff_base_reports_changed_blocks_only(self) -> None:
        pathlib.Path("doc.md").write_text(f"Short, now {words(45)}\n\n{words(50)}\n")
        pathlib.Path("other.md").write_text(words(50) + "\n")  # untracked, so not in the diff
        code, out = self.run_main("--diff-base", "HEAD", "--github")
        self.assertEqual(code, 0)
        self.assertEqual(len(out.splitlines()), 1)
        self.assertTrue(out.startswith("::warning file=doc.md,line=1,"))

    def test_summary_appends_to_a_file(self) -> None:
        path = pathlib.Path(self.dir.name, "summary.md")
        path.write_text("before\n")
        self.run_main("--summary", str(path))
        self.assertTrue(path.read_text().startswith("before\n## Documentation check (report only)\n"))
        self.assertIn("1 warnings in 1 of 1 Markdown files", path.read_text())


class StandardTest(unittest.TestCase):
    """The limits the script counts are the limits the standard states."""

    def test_writing_standard_states_the_limits(self) -> None:
        text = " ".join((REPO / "docs/writing.md").read_text().split())
        self.assertIn(
            f"A block of prose has at most {checker.MAX_SENTENCES} sentences and at most {checker.MAX_WORDS} words.",
            text,
        )
        self.assertIn(f"Split a sentence of more than {checker.MAX_SENTENCE_WORDS} words.", text)
        self.assertIn(f"`{checker.CURRENT_STATE}`", text)

    def test_both_state_the_identifier_rule_in_the_same_words(self) -> None:
        rule = (
            "outside the document that defines it, an identifier{} may appear only as a link to its definition, "
            "and the same sentence must state the behaviour or rule in plain words. The ID is a pointer for "
            "traceability, never the explanation, so a reader who does not follow the link still understands "
            "the sentence. Inside the defining document, IDs are anchors as usual."
        )
        writing = " ".join((REPO / "docs/writing.md").read_text().split())
        guidance = " ".join((REPO / "memoria.toml").read_text().split())
        self.assertIn(rule.format(", such as a finding ID,"), writing.replace("Outside the document", "outside the document"))
        self.assertIn(rule.format(" (such as a finding ID)"), guidance)

    def test_both_apply_the_identifier_rule_to_code_comments(self) -> None:
        # The rule has no exception for prose in comments: the link takes the
        # comment's form, and the plain statement shares its sentence.
        form = (
            "in a code comment, the link is the repository path of the defining file with its anchor, "
            "right after the ID, in the sentence that states the rule"
        )
        for name in ("docs/writing.md", "memoria.toml"):
            text = " ".join((REPO / name).read_text().split())
            with self.subTest(name):
                self.assertIn(form, text)
                self.assertNotIn("same comment", text)

    def test_memoria_guidance_states_the_limits(self) -> None:
        text = " ".join((REPO / "memoria.toml").read_text().split())
        self.assertRegex(
            text,
            re.escape(f"at most {checker.MAX_SENTENCES} sentences and at most {checker.MAX_WORDS} words")
            + r".*"
            + re.escape(f"at most {checker.MAX_SENTENCE_WORDS} words"),
        )


if __name__ == "__main__":
    unittest.main()
