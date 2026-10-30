# Memoria

## What Memoria is here

[Memoria](https://github.com/viktordanov/rs-memoria) keeps READMEs connected
to the source they describe, with a check enforced in CI. A README's scope is
its own folder and everything below it. Linking or importing a README that
lives in a subfolder hands that subfolder off, so the nested README covers it
instead. Memoria detects changes that need review; contributors check whether
the prose still matches the code. This guide covers repository policy; the
[Memoria skill](../.claude/skills/memoria/SKILL.md) (also installed for Codex
under `.agents/skills/memoria/`) covers the procedure.

## When you interact with it

Use Memoria after any change in a README's scope and before opening a pull
request. When reviewing someone else's pull request, check the affected README
claims and the documentation gate too.

Memoria needs a real Git worktree: its commands refuse to run where there is no
`.git`. If you work in a Jujutsu workspace without one, run the Memoria
commands in a throwaway `git worktree` of your pushed branch and copy the
updated `memoria.lock` back.

## The per-session flow

1. **Status.** Run `memoria status` to see scope coverage and pending reviews.
2. **Review.** Follow the review plan's `next_action`: render outdated imports
   or prepare the next ready README for review with `memoria review
   <README.md>`. Cross-check the README's prose against the source in its scope
   and against the [writing standard](writing.md). Edit claims that have
   drifted, then refresh the review packet.
3. **Acknowledge.** Run `memoria ack` with a note that states what you verified.
4. **Check.** Repeat until the plan is empty and `memoria check` passes. Run it
   bare and read its exit code; piping it through `head` or `grep` hides
   failures.

Do this after your last edit. Changing any file in a README's scope after the
ack re-opens that README's review.

To ask why a README is pending without preparing a packet, run
`memoria explain <README.md>`. It is read-only and records no review. Review
packets belong outside the repository so they do not become documentation
inputs.

## CI gate

The [Memoria documentation workflow](../.github/workflows/memoria.yml) installs
the CLI with the first-party `setup-memoria` Action and runs `memoria check` on
`ubuntu-24.04`. A non-zero exit fails the job. A pending-review diagnostic means
a README has not been reviewed against its current inputs; it does not by itself
prove the prose is wrong. Passing the gate validates recorded reviews, not the
accuracy of the prose.

Memoria generates and owns that workflow, recording the expected text in
`.github/memoria-workflows/memoria.yml.json`. Change it only with
`memoria integrations github upgrade --apply`; a hand edit makes Memoria stop
managing the file. The runner label is exact because the Action supports Ubuntu
24.04 only.

## Renovate bumps

**Current state: installed, awaiting activation.** The
[auto-ack workflow](../.github/workflows/memoria-auto-ack.yml) is in place but
does not acknowledge anything yet. It pushes with the `MEMORIA_AUTO_ACK_TOKEN`
repository secret, a fine-grained token with read and write access to this
repository's contents, and the repository owner has not provisioned that
secret. Until they do, the workflow fails at its first step on every Renovate
pull request.

Until then, acknowledge a Renovate pull request by hand like any other: check
out its branch in a Git worktree, follow the per-session flow above, and push
the updated `memoria.lock` to the Renovate branch.

Once the secret exists, the workflow acknowledges README reviews on Renovate
pull requests whose only changes are dependency version bumps in `go.mod` and
`go.sum` or action-pin bumps in workflow files, using the
[scripts](../scripts/README.md) in this repository. Anything else still needs a
manual ack. It installs the same Memoria version from the same Action ref as the
gate, pinned additionally by archive digest; bump both workflows and your local
install together.

## Policy changes

A change to the writing rules makes no README pending. The rules are the
[writing standard](writing.md), the [glossary](glossary.md) and the guidance
in [memoria.toml](../memoria.toml). `memoria guidance --changed` lists the
READMEs that were reviewed under older rules.

To request a review of those READMEs, run `memoria invalidate all --reason
"..."` for a repository-wide rule, or `memoria invalidate subtree:<dir>
--reason "..."` for a narrower one. Routine drift from ordinary code changes
flows through the per-session flow instead.

[memoria.toml](../memoria.toml) holds the review checklist and names the
writing standard and the glossary as guidance files: Memoria shows them to
every reviewer, and never counts them as inputs of a README. Its ignore list
excludes jj internal state, build output, `go.sum`, `renovate.json`, and agent
process records from review inputs, so a checksum-only dependency update or a
Renovate config migration needs no README review; `go.mod` remains a review
input.
