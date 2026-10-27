#!/usr/bin/env bash
#
# Auto-ack pending README reviews when a Renovate branch's only changes
# are provably-safe dependency bumps: go.sum, go.mod with version
# literals on existing requirements, and action-pin (`uses:`) bumps in
# workflow files.
#
# Contract: on entry, HEAD is the Renovate branch tip. On success, this
# script has either (a) done nothing (change wasn't safe or nothing was
# pending) or (b) staged an updated memoria.lock. The caller (workflow)
# commits and pushes.
#
# Exits non-zero only on internal errors. "Not safe to auto-ack" is an
# expected outcome that leaves the branch untouched.
set -euo pipefail

BASE_REF="${BASE_REF:-origin/main}"
# When the workflow invokes this script from main's checkout with the
# PR head worktree as cwd (the `pull_request_target` topology), the
# workflow sets SCRIPTS_ROOT to main's `scripts/` so this script never
# resolves its helpers via cwd, which would be untrusted PR-head content.
# For local or manual use, fall back to `scripts/` relative to cwd.
SCRIPTS_ROOT="${SCRIPTS_ROOT:-scripts}"

# ---- 1. What actually changed vs base? -------------------------------

# actions/checkout already fetched full history (fetch-depth: 0). Only
# refresh the base ref itself; do NOT pass --depth here (a shallow fetch
# on a full clone creates .git/shallow and can break the merge-base for
# `git diff BASE...HEAD`). Strip the `origin/` prefix so `git fetch`
# receives a remote-side ref name.
git fetch origin "${BASE_REF#origin/}"
changed=$(git diff --name-only "$BASE_REF"...HEAD)
if [ -z "$changed" ]; then
    echo "auto-ack: no changes vs $BASE_REF, nothing to do"
    exit 0
fi

# ---- 2. Restrict changed paths to the allowlist ----------------------

# Anything outside the following families is a structural change we
# refuse to auto-ack. The list is deliberately narrower than what
# Renovate may touch: we reject every file we do not understand.
#
#   1. go.mod and go.sum, at the root or in a nested module.
#   2. `.github/workflows/*.yml|*.yaml`. Renovate publishes action-pin
#      bumps by updating `uses: <owner>/<repo>@<sha>` lines. Every such
#      changed file is structure-checked by `check-workflow-yaml-diff.py`
#      below so only diffs whose ONLY changes are `uses:` values pass.
non_safe=$(printf '%s\n' "$changed" \
    | grep -vE '^(([^/]+/)*go\.(mod|sum)|\.github/workflows/[^/]+\.ya?ml)$' \
    || true)
if [ -n "$non_safe" ]; then
    printf 'auto-ack: SKIP — Renovate touched paths outside the safe allowlist:\n%s\n' "$non_safe"
    exit 0
fi

# ---- 3a. For every go.mod touched, verify the diff is version-only --

changed_modfiles=$(printf '%s\n' "$changed" | grep -E '(^|/)go\.mod$' || true)
if [ -n "$changed_modfiles" ]; then
    while IFS= read -r modfile; do
        if ! python3 "$SCRIPTS_ROOT/check-go-mod-diff.py" "$BASE_REF" "$modfile"; then
            echo "auto-ack: SKIP — $modfile diff isn't pure version-literal bumps; human ack required"
            exit 0
        fi
    done <<<"$changed_modfiles"
fi

# ---- 3b. For every workflow YAML touched, verify diff is uses-pin only

changed_workflows=$(printf '%s\n' "$changed" \
    | grep -E '^\.github/workflows/[^/]+\.ya?ml$' || true)
if [ -n "$changed_workflows" ]; then
    while IFS= read -r workflow; do
        if ! python3 "$SCRIPTS_ROOT/check-workflow-yaml-diff.py" "$BASE_REF" "$workflow"; then
            echo "auto-ack: SKIP — $workflow diff isn't pure action-pin (uses:) bumps; human ack required"
            exit 0
        fi
    done <<<"$changed_workflows"
fi

# ---- 4. Ack every pending README ------------------------------------

# memoria's structured output lists exactly which READMEs are pending
# and why. We only ack ones whose sole cause is `input_changed` — if
# a README shows any other cause (guidance_changed, policy_changed,
# imports_changed with structural effects, missing_evidence, etc.) we
# bail so a human can adjudicate. This is defense-in-depth: even inside
# the safe-diff branch above, we still refuse to auto-ack a README that
# claims something we don't recognize.
review_json=$(memoria --root . review --format json 2>/dev/null || true)
if [ -z "$review_json" ]; then
    echo "auto-ack: memoria review returned no JSON; nothing to ack"
    exit 0
fi

pending_paths=$(printf '%s' "$review_json" \
    | python3 -c '
import json, sys
top = json.load(sys.stdin)
# The review command emits {command, data, diagnostics, ok, schema_version}.
# `data.tasks[]` lists every README under review, each with `document`,
# `status`, `guidance_changed`, and a `causes` list whose entries carry
# a `code`. Auto-ack ONLY when every gate agrees:
#   - status == "pending" (there is actually something to ack)
#   - guidance_changed is false (the guidance itself did not shift; if it
#     did, an authorized review is required per SKILL.md §Rules)
#   - every cause code is `input_changed` (no other freshness signal)
tasks = top.get("data", {}).get("tasks", [])
for t in tasks:
    if t.get("status") != "pending":
        continue
    if t.get("guidance_changed"):
        continue
    causes = t.get("causes") or []
    codes = {c.get("code") for c in causes if isinstance(c, dict)}
    if codes and codes.issubset({"input_changed"}):
        print(t.get("document"))
')

if [ -z "$pending_paths" ]; then
    echo "auto-ack: no READMEs are pending purely from input_changed; nothing to do"
    exit 0
fi

commit_subject=$(git log -1 --format=%s HEAD)
note="auto-ack: safe dependency version bumps (${commit_subject})"

# memoria ack needs a fresh packet snapshot + its 21-byte token per README.
# `memoria review <path> --format json` emits data.token and the packet
# body; feed the whole JSON to ack via --packet=- to record the ack.
while IFS= read -r readme; do
    echo "auto-ack: memoria ack $readme"
    packet=$(memoria --root . review "$readme" --format json)
    token=$(printf '%s' "$packet" | python3 -c 'import json, sys; print(json.load(sys.stdin)["data"]["token"])')
    printf '%s' "$packet" | memoria --root . ack \
        --packet - \
        --token "$token" \
        --result no-update \
        --reviewer github-actions \
        --note "$note" \
        "$readme"
done <<<"$pending_paths"

# ---- 5. Confirm the ack made memoria happy --------------------------

memoria --root . check

# ---- 6. Stage the resulting memoria.lock (only) ---------------------

# The caller (workflow) still runs a stricter allowlist check on the
# entire tree before committing, but stage here so `git diff --cached`
# tells the workflow whether there is anything to commit.
if ! git diff --quiet -- memoria.lock; then
    git add memoria.lock
fi
echo "auto-ack: done"
