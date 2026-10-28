# Windmill semantics reference

**Current state:** first edition, written against Windmill `v1.767.0`, revised
on 2026-10-28 with recording batches E3a to E3d. This README covers
`docs/semantics/`: this index and the area files it links. The reference
records the Windmill behaviour windmill-lite must reproduce, as numbered
findings with a scope, a confidence level and a status. Expected outputs were
first derived from the pinned source; recording batches E3a to E3d (design
record 0012) have since recorded 311 of the catalogue's cases on the CE
oracle, and the findings and cases they settled or corrected say so in dated
revision and correction notes. Each pending recording is tracked as an open
question. Disputed findings are shown with both claims and are not settled.
windmill-lite's code and tests cite these findings; this reference describes
Windmill only.

## Pinned version

| | |
|---|---|
| Windmill release | `v1.767.0` |
| Commit | `6de4ec0f665ad856002495c4d86269105ec28ad7` |
| Source pointers | `path:line @ v1.767.0`, relative to the root of the [Windmill repository](https://github.com/windmill-labs/windmill/tree/v1.767.0) |

The reference claims nothing about other Windmill versions. Where the source
and the [public documentation](https://www.windmill.dev/docs) disagree, the
finding follows the source at the pin and notes the disagreement.

## Who writes it and who reads it

The semantics team writes this reference. It may read Windmill's AGPLv3 source
at the pin and describes behaviour in its own words: rules, tables, decision
lists, input-to-output examples, and source pointers. No Windmill code is
reproduced here.

The build team (everyone who designs, reviews or implements windmill-lite)
works from this reference, the public documentation, the Apache-2.0 parts of
Windmill and observed behaviour. **Source pointers are for the semantics team
to verify and update findings. Build-team members do not follow them into
AGPL or proprietary files**: Windmill is AGPLv3 unless a part says otherwise,
and [licensing.md](licensing.md) maps which parts do.
[AGENTS.md](../../AGENTS.md) holds the binding contributor rules.

## Areas

| File | What it covers | Finding IDs |
|---|---|---|
| [openflow.md](openflow.md) | The OpenFlow model as Windmill decodes and normalises it, deploy-time validation, the `wmill sync` folder layout, and prior art for parsing it. | `OF-`, `SY-`, `PA-` |
| [execution.md](execution.md) | How a flow advances step by step: `flow_input`, `results`, `previous_result`, transforms, `skip_if`, `stop_after_if`, early return, sleep, timeout, retry, `continue_on_error`, error shapes, failure and preprocessor modules, cancellation. | `FX-` |
| [control-flow.md](control-flow.md) | `forloopflow`, `whileloopflow`, `branchone`, `branchall`, subflows and `identity`; result shapes, failure handling and `results` visibility across container boundaries. | `CF-` |
| [expressions.md](expressions.md) | The JavaScript expression evaluator: engine and limits, fast paths, every expression site and its scope, static values and `$var:`/`$res:` references. | `JE-` |
| [languages.md](languages.md) | How a script runs, by language: Bun in depth (arguments, results, errors, environment, bundling and imports), codebase bundles, the native runtime of `nativets` and `bunnative` steps, and Deno, Python, Go and Bash in summary. | `WR-` |
| [client-api.md](client-api.md) | The `wmill` client surface a script can call, the endpoint behind each call, and the platform services windmill-lite must provide. | `WA-` |
| [oracle.md](oracle.md) | Differential testing against a real Windmill CE `v1.767.0`: topology, bootstrap, running flows, capturing per-step results, normalisation, fixture format, and observations recorded so far. | `OR-` |
| [licensing.md](licensing.md) | The licence map of Windmill's repository, what windmill-lite may read, depend on or copy, and the decision on using the CE image as a test oracle. | `LIC-` |
| [conformance.md](conformance.md) | The catalogue of conformance cases drawn from every finding, grouped by area and tagged by scope: the seed of the shared Go and harness conformance suite. | case IDs |

### Counts

| Area | Findings | `core` | `later` | `out` | `EE` | Not settled | Question entries | Conformance cases |
|---|---|---|---|---|---|---|---|---|
| [openflow.md](openflow.md) | 51 | 42 | 8 | 1 | 0 | 2 | 13 | 143 |
| [execution.md](execution.md) | 57 | 46 | 9 | 2 | 0 | 2 | 20 | 124 |
| [control-flow.md](control-flow.md) | 36 | 30 | 5 | 0 | 1 | 0 | 18 | 86 |
| [expressions.md](expressions.md) | 57 | 40 | 14 | 3 | 0 | 0 | 12 | 170 |
| [languages.md](languages.md) | 63 | 45 | 16 | 0 | 2 | 2 | 27 | 183 |
| [client-api.md](client-api.md) | 41 | 28 | 11 | 1 | 1 | 0 | 13 | 93 |
| [oracle.md](oracle.md) | 40 | 37 | 2 | 0 | 1 | 6 | 18 | 82 |
| [licensing.md](licensing.md) | 3 | 3 | 0 | 0 | 0 | 0 | 1 | 3 |
| **Total** | **348** | **271** | **65** | **7** | **5** | **12** | **122** | **884** |

Findings are counted by their primary scope tag; withdrawn stubs are not
counted (expressions.md has one, `JE-4`). "Not settled" counts findings whose
status is `DISPUTED`, `needs-evidence` or `open` (listed under
[Findings that are not settled](#not-settled)). Twenty-two findings are
`drafted` and not yet independently checked; they are not counted as
unsettled. They are the findings this revision added or changed from the
pinned source alone, with no recording behind the change:
[OF-14](openflow.md#of-14), [SY-4](openflow.md#sy-4),
[SY-5](openflow.md#sy-5), [SY-7](openflow.md#sy-7), [SY-8](openflow.md#sy-8),
[SY-10](openflow.md#sy-10), [SY-11](openflow.md#sy-11),
[SY-13](openflow.md#sy-13), [SY-15](openflow.md#sy-15) and the new
[SY-16](openflow.md#sy-16) to [SY-19](openflow.md#sy-19);
[FX-51](execution.md#fx-51); [CF-30](control-flow.md#cf-30);
[JE-50](expressions.md#je-50) to [JE-52](expressions.md#je-52); and
[WR-51](languages.md#wr-51) to [WR-54](languages.md#wr-54). A few confirmed
findings carry a drafted part, which their status line names (for example
[WR-15](languages.md#wr-15)). The answers to semantics requests 1 to 3
([JE-48](expressions.md#je-48), [JE-49](expressions.md#je-49),
[WR-40](languages.md#wr-40) to [WR-50](languages.md#wr-50)) passed their
independent check and are `confirmed`. Question entries include answered
questions, which keep their numbers; openflow.md's `Q-OF-7` entry points to
licensing.md, where that question lives. 311 cases are `oracle-observed`,
and seven cases are windmill-lite `policy` checks rather than Windmill
behaviour ([conformance.md](conformance.md#summary)).

<a id="legend"></a>
## Legend

Every finding opens with one tag line:
`**Scope:** … · **Confidence:** … · **Status:** …`.

### Scope

| Tag | Meaning |
|---|---|
| `core` | windmill-lite's first release must match this behaviour exactly. |
| `later` | Needed to run general Windmill folders, but not for the first release. |
| `out` | Out of scope: Windmill's scheduling and platform concerns (job queue, worker tags, priorities, concurrency limits, job rows and UI, durable suspend and approval). Where it matters, the finding still says what a flow does when it meets the feature. |
| `EE` | Enterprise Edition behaviour. Out of scope unless it is observable on the Community Edition. |

A finding whose parts differ in scope lists its primary tag first and the
split after it, for example `core (loader rule); later (lock generation)`. The
primary tag is the most demanding one that applies (`core` before `later`
before `out`).

### Confidence

| Level | Meaning |
|---|---|
| `high` | The code path was read and traced at the pin. |
| `medium` | The code was read but the path was not traced end to end. |
| `low` | Inferred. |

A qualifier in parentheses gives the provenance: `source-traced` (no oracle
recording yet), `oracle pending` (an open question asks for a recording), or
`oracle-observed` (recorded on a running Windmill CE `v1.767.0`; see
[oracle.md](oracle.md)).

### Status

| Status | Meaning |
|---|---|
| `confirmed` | An independent check against the pinned source confirmed the finding as written. |
| `drafted` | The semantics team wrote the finding from the pinned source; the independent check has not run yet. Build on it as stated; if the check corrects it, the finding is edited in place. |
| `DISPUTED` | An independent check against the pinned source disputed the latest text: the final check of the study the finding comes from, or a later review of this reference (the entry says which). The entry shows the rule as drafted and the correction side by side, and an open question tracks it. Neither side is settled behaviour: do not build on it until the question is closed. |
| `needs-evidence` | The finding lacks the evidence its claim needs. |
| `open` | The behaviour itself is undecided; see the linked open question. |
| `withdrawn`, `merged into …` | The ID is kept so it is never reused; the entry has no rule of its own. |

### Finding layout

Each finding has an anchor equal to its lower-cased ID (`execution.md#fx-12`)
and these parts:

- **Rule:** the behaviour, precise enough to implement and test.
- **Evidence:** source pointers at the pin, and public documentation links.
- **Conformance:** links to the finding's cases in
  [conformance.md](conformance.md).
- **Note:** cross-references and qualifications.
- **Design note (not Windmill behaviour):** a proposal for windmill-lite made
  while studying the behaviour. It binds nothing; the design decides.

## Citing findings

Cite a finding by its ID from code and tests, for example `// spec: FX-12`,
and a conformance case by its case ID, for example `// spec: CF-4/b`.

- IDs are stable. A revised finding keeps its ID, numbering has gaps, and a
  withdrawn ID is never reused.
- IDs with a `V` (`FX-V3`) were added by an independent check of the draft;
  they are ordinary findings.
- Oracle findings use `OR-` so that they do not collide with the OpenFlow
  `OF-` findings.

## Open questions

Each area file ends with its open questions, numbered `Q-<prefix>-<n>` (for
example `Q-FX-15`), with an anchor equal to the lower-cased ID. A question
says what would settle it: usually an oracle recording, sometimes a further
source trace or a human decision. Answered questions keep their number and
point to the finding that answers them.

<a id="not-settled"></a>
### Findings that are not settled

Do not implement against these until the tracking question closes. Each entry
shows the drafted rule and the competing claim side by side.

The 2026-10-28 revision settled nine findings that this table used to list,
each now `confirmed` with a revision note that keeps the losing reading:
[FX-10](execution.md#fx-10) and [FX-V6](execution.md#fx-v6)
([Q-FX-15](execution.md#q-fx-15)), [FX-33](execution.md#fx-33)
([Q-FX-19](execution.md#q-fx-19)), [FX-40](execution.md#fx-40)
([Q-FX-17](execution.md#q-fx-17)), [FX-42](execution.md#fx-42)
([Q-FX-16](execution.md#q-fx-16)), [CF-12](control-flow.md#cf-12)
([Q-CF-14](control-flow.md#q-cf-14)), [CF-19](control-flow.md#cf-19) and
[CF-V6](control-flow.md#cf-v6) ([Q-CF-16](control-flow.md#q-cf-16)), all by
oracle recordings, and [CF-8](control-flow.md#cf-8)
([Q-CF-15](control-flow.md#q-cf-15)) by a source re-check together with the
recorded by-path halves.

| Finding | Status | What is contested | Tracked by |
|---|---|---|---|
| [OF-3](openflow.md#of-3) | `DISPUTED` | Which form of a flow (lite or full) a run reads: one selection, or separate selections at enqueue and in the worker. | [Q-OF-11](openflow.md#q-of-11) |
| [OF-V5](openflow.md#of-v5) | `open` | The two-phase selection rule: the competing claim against OF-3. | [Q-OF-11](openflow.md#q-of-11) |
| [FX-36](execution.md#fx-36) | `needs-evidence` | Whether a non-object final result (an array) decodes positionally as response metadata. | [Q-FX-12](execution.md#q-fx-12) |
| [FX-49](execution.md#fx-49) | `DISPUTED` | How many times a re-evaluated `skip_expr` runs at cursor 0, and the outcome. | [Q-FX-11](execution.md#q-fx-11) |
| [WR-16](languages.md#wr-16) | `DISPUTED` (`later` part) | How a custom stream `replace` value becomes text, and how stream lines are classified. | [Q-WR-15](languages.md#q-wr-15) |
| [WR-28](languages.md#wr-28) | `DISPUTED` (`later` part) | Which dynamic imports the import rewrite reaches. | [Q-WR-16](languages.md#q-wr-16) |
| [OR-11](oracle.md#or-11) | `DISPUTED` | The step identities the result walker assigns. | [Q-OR-17](oracle.md#q-or-17) |
| [OR-14](oracle.md#or-14) | `DISPUTED` | Normalisation placeholders, which inherit the identity collisions. | [Q-OR-17](oracle.md#q-or-17) |
| [OR-V3](oracle.md#or-v3) | `DISPUTED` | Whether normalisation keeps distinct steps distinct. | [Q-OR-17](oracle.md#q-or-17) |
| [OR-V8](oracle.md#or-v8) | `DISPUTED` | A collision-free encoding of step identities. | [Q-OR-17](oracle.md#q-or-17) |
| [OR-15](oracle.md#or-15) | `needs-evidence` | Whether the probe catalogue fills the fixture format as exact executable fixtures. | [Q-OR-16](oracle.md#q-or-16) |
| [OR-V9](oracle.md#or-v9) | `needs-evidence` | The transform bindings of the recorded probe recipes. | [Q-OR-16](oracle.md#q-or-16) |

## Changing the reference

1. Only the semantics team edits findings. A build-team question reaches it
   as a request to cover a behaviour, never as a source excerpt.
2. A new finding takes the next free number in its prefix. A correction edits
   the finding in place and keeps its ID.
3. When an oracle recording settles an open question, update the finding's
   confidence qualifier, the provenance of its conformance cases, and the
   question's entry.
