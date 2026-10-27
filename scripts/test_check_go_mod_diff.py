"""Tests for check-go-mod-diff.py. Run: python3 -m unittest discover -s scripts"""

from __future__ import annotations

import importlib.util
import pathlib
import unittest

_spec = importlib.util.spec_from_file_location(
    "check_go_mod_diff", pathlib.Path(__file__).with_name("check-go-mod-diff.py")
)
checker = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(checker)

BASE = """module example.com/fixture

go 1.27

toolchain go1.27.1

require (
\texample.com/alpha v1.0.0
\texample.com/beta v0.1.0 // indirect
)

require example.com/gamma v2.0.0
"""


def unsafe(head: str) -> str | None:
    return checker.unsafe_change(checker.parse(BASE), checker.parse(head))


class CheckGoModDiffTest(unittest.TestCase):
    def test_identical_is_safe(self) -> None:
        self.assertIsNone(unsafe(BASE))

    def test_require_version_bumps_are_safe(self) -> None:
        head = BASE.replace("alpha v1.0.0", "alpha v1.1.0").replace("gamma v2.0.0", "gamma v2.0.1")
        self.assertIsNone(unsafe(head))

    def test_concrete_toolchain_bump_is_safe(self) -> None:
        self.assertIsNone(unsafe(BASE.replace("toolchain go1.27.1", "toolchain go1.27.3")))
        self.assertIsNone(unsafe(BASE.replace("toolchain go1.27.1", "toolchain go1.28rc1")))

    def test_toolchain_policy_operands_are_unsafe(self) -> None:
        # Regression: `default` passed as a "version bump" because only the
        # token count was compared.
        for operand in ("default", "local", "go1.27", "go1.27.3-custom", "go1.27.3+auto"):
            with self.subTest(operand=operand):
                self.assertIsNotNone(unsafe(BASE.replace("toolchain go1.27.1", f"toolchain {operand}")))

    def test_toolchain_from_default_is_unsafe(self) -> None:
        base = BASE.replace("toolchain go1.27.1", "toolchain default")
        head = BASE.replace("toolchain go1.27.1", "toolchain go1.27.3")
        self.assertIsNotNone(checker.unsafe_change(checker.parse(base), checker.parse(head)))

    def test_structural_changes_are_unsafe(self) -> None:
        cases = {
            "go line": BASE.replace("go 1.27", "go 1.28"),
            "module swap": BASE.replace("example.com/alpha", "example.com/other"),
            "indirect toggle": BASE.replace(" // indirect", ""),
            "added replace": BASE + "replace example.com/alpha => ../alpha\n",
            "added require": BASE + "require example.com/delta v1.0.0\n",
            "removed toolchain": BASE.replace("toolchain go1.27.1\n", ""),
        }
        for name, head in cases.items():
            with self.subTest(name):
                self.assertIsNotNone(unsafe(head))


if __name__ == "__main__":
    unittest.main()
