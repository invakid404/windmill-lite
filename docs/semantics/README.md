# Windmill semantics reference

**Current state:** first edition, written against Windmill `v1.767.0`, revised
on 2026-10-28 with recording batches E3a to E3d, and twice on 2026-10-29:
with recording batch E3e and the build milestones' questions, then (SC3)
with the questions the engine, platform and follow-up milestones left open
(N1b, N2, N3a, N3b, PF1 to PF3, FU4, FU5), answered from the pinned source,
with the recordings only the oracle can make requested from a future batch
E3f ([design README](../design/README.md#recording-requests-for-batch-e3f)),
and then (SC4) with the questions milestones N3c, NI, N4, N5, F1, E4b and
FU6 left open and the open questions SC3 listed, answered from the pinned
source, together with the oracle recordings milestones E1, N2, N3c, NI,
N5 and PF1 to PF3 had committed but the catalogue had not yet folded in,
and then on 2026-10-30 (SC5) with the results of recording batch E3f (a
native-step session and the recordings SC3 and SC4 requested), the
questions milestones FU8 and F3h left open and the open questions SC4
listed whose answer is in the pinned source, with what only a running
Windmill can still show requested from the next run of batch E3f
([design README](../design/README.md#recording-requests-for-a-future-batch-e3f-sc5)).
This README covers
`docs/semantics/`: this index and the area files it links. The reference
records the Windmill behaviour windmill-lite must reproduce, as numbered
findings with a scope, a confidence level and a status. Expected outputs were
first derived from the pinned source; recording batches E3a to E3f (design
record 0012) and the recordings the build milestones committed with their
fixtures have since recorded 654 of the catalogue's cases on the CE
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
| [openflow.md](openflow.md) | 51 | 42 | 8 | 1 | 0 | 0 | 13 | 149 |
| [execution.md](execution.md) | 57 | 46 | 9 | 2 | 0 | 0 | 21 | 161 |
| [control-flow.md](control-flow.md) | 37 | 30 | 6 | 0 | 1 | 0 | 19 | 124 |
| [expressions.md](expressions.md) | 57 | 40 | 14 | 3 | 0 | 0 | 17 | 195 |
| [languages.md](languages.md) | 64 | 45 | 17 | 0 | 2 | 0 | 28 | 212 |
| [client-api.md](client-api.md) | 41 | 28 | 11 | 1 | 1 | 0 | 13 | 108 |
| [oracle.md](oracle.md) | 40 | 37 | 2 | 0 | 1 | 0 | 18 | 88 |
| [licensing.md](licensing.md) | 3 | 3 | 0 | 0 | 0 | 0 | 1 | 3 |
| **Total** | **350** | **271** | **67** | **7** | **5** | **0** | **130** | **1040** |

Findings are counted by their primary scope tag; withdrawn stubs are not
counted (expressions.md has one, `JE-4`). "Not settled" counts findings whose
status is `DISPUTED`, `needs-evidence` or `open` (listed under
[Findings that are not settled](#not-settled)). Sixteen findings are
`drafted` and not yet independently checked; they are not counted as
unsettled. They are the findings a revision added or changed from the
pinned source alone, with no recording behind the change:
[OF-14](openflow.md#of-14), [SY-4](openflow.md#sy-4),
[SY-5](openflow.md#sy-5), [SY-7](openflow.md#sy-7), [SY-8](openflow.md#sy-8),
[SY-10](openflow.md#sy-10), [SY-11](openflow.md#sy-11),
[SY-13](openflow.md#sy-13), [SY-15](openflow.md#sy-15) and
[SY-16](openflow.md#sy-16) to [SY-19](openflow.md#sy-19), whose
recordings need a harness that pushes sync trees as runs; and
[WR-51](languages.md#wr-51) to [WR-53](languages.md#wr-53). The SC5
revision of 2026-10-30 moved eight findings that were wholly drafted to
`confirmed` on batch E3f's recordings, some keeping a drafted part that
their status line names: [FX-51](execution.md#fx-51),
[CF-30](control-flow.md#cf-30), [CF-31](control-flow.md#cf-31),
[JE-50](expressions.md#je-50) to [JE-52](expressions.md#je-52),
[WR-54](languages.md#wr-54) and [WR-55](languages.md#wr-55). Confirmed findings may
carry a drafted part, which their status line names (for example
[WR-15](languages.md#wr-15)). The 2026-10-29 revision added such parts to
[OF-2](openflow.md#of-2), [FX-42](execution.md#fx-42),
[FX-50](execution.md#fx-50), [JE-11](expressions.md#je-11),
[JE-16](expressions.md#je-16), [JE-18](expressions.md#je-18),
[JE-44](expressions.md#je-44), [WR-22](languages.md#wr-22),
[WR-26](languages.md#wr-26), [WR-28](languages.md#wr-28), [WR-40](languages.md#wr-40),
[WR-49](languages.md#wr-49), [WR-50](languages.md#wr-50),
[OR-21](oracle.md#or-21) and [OR-27](oracle.md#or-27). The SC3 revision
of the same day added or extended drafted parts in
[OF-3](openflow.md#of-3), [OF-17](openflow.md#of-17),
[FX-13](execution.md#fx-13), [FX-26](execution.md#fx-26),
[FX-27](execution.md#fx-27), [FX-48](execution.md#fx-48),
[FX-50](execution.md#fx-50), [CF-7](control-flow.md#cf-7),
[CF-23](control-flow.md#cf-23), [CF-25](control-flow.md#cf-25),
[JE-19](expressions.md#je-19), [JE-20](expressions.md#je-20),
[JE-42](expressions.md#je-42), [JE-49](expressions.md#je-49),
[WR-11](languages.md#wr-11), [WR-22](languages.md#wr-22),
[WR-37](languages.md#wr-37), [WR-50](languages.md#wr-50),
[WA-4](client-api.md#wa-4), [WA-11](client-api.md#wa-11),
[WA-12](client-api.md#wa-12), [WA-14](client-api.md#wa-14),
[WA-16](client-api.md#wa-16), [WA-19](client-api.md#wa-19),
[WA-42](client-api.md#wa-42), [WA-V2](client-api.md#wa-v2),
[OR-7](oracle.md#or-7), [OR-21](oracle.md#or-21),
[OR-24](oracle.md#or-24) and [OR-V7](oracle.md#or-v7), and extended the
drafted findings [SY-5](openflow.md#sy-5), [SY-7](openflow.md#sy-7),
[SY-19](openflow.md#sy-19), [FX-51](execution.md#fx-51),
[WR-52](languages.md#wr-52) and [WR-54](languages.md#wr-54). The SC4
revision of the same day added or extended drafted parts in
[FX-27](execution.md#fx-27), [FX-29](execution.md#fx-29),
[FX-30](execution.md#fx-30), [FX-38](execution.md#fx-38),
[FX-40](execution.md#fx-40), [FX-42](execution.md#fx-42),
[CF-7](control-flow.md#cf-7), [CF-12](control-flow.md#cf-12),
[CF-16](control-flow.md#cf-16), [CF-17](control-flow.md#cf-17),
[CF-21](control-flow.md#cf-21), [CF-22](control-flow.md#cf-22),
[CF-25](control-flow.md#cf-25), [CF-28](control-flow.md#cf-28),
[JE-44](expressions.md#je-44), [WR-40](languages.md#wr-40),
[WA-11](client-api.md#wa-11), [WA-15](client-api.md#wa-15) and
[OR-27](oracle.md#or-27), and extended the drafted finding
[WR-55](languages.md#wr-55). The SC5 revision of 2026-10-30 removed the
drafted parts batch E3f recorded (each finding's revision note says
which), and added or extended drafted parts, from the source alone, in
[FX-26](execution.md#fx-26), [CF-7](control-flow.md#cf-7),
[CF-25](control-flow.md#cf-25), [JE-42](expressions.md#je-42),
[WR-11](languages.md#wr-11), [WR-27](languages.md#wr-27),
[WR-46](languages.md#wr-46), [WR-50](languages.md#wr-50) and
[WA-19](client-api.md#wa-19). The answers to semantics requests 1 to 3
([JE-48](expressions.md#je-48), [JE-49](expressions.md#je-49),
[WR-40](languages.md#wr-40) to [WR-50](languages.md#wr-50)) passed their
independent check and are `confirmed`. Question entries include answered
questions, which keep their numbers; openflow.md's `Q-OF-7` entry points to
licensing.md, where that question lives. 654 cases are `oracle-observed`,
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

The 2026-10-29 revision settled six more, all by recording batch E3e and
each now `confirmed` with a revision note that keeps the losing reading:
[FX-36](execution.md#fx-36) ([Q-FX-12](execution.md#q-fx-12): an array
result decodes positionally), [FX-49](execution.md#fx-49)
([Q-FX-11](execution.md#q-fx-11)), [WR-16](languages.md#wr-16)
([Q-WR-15](languages.md#q-wr-15)), [WR-28](languages.md#wr-28)
([Q-WR-16](languages.md#q-wr-16)), and [OR-15](oracle.md#or-15) and
[OR-V9](oracle.md#or-v9) ([Q-OR-16](oracle.md#q-or-16)).

The SC3 revision of 2026-10-29 settled two more from the pinned source,
with no recording: [OF-3](openflow.md#of-3) is `confirmed` with a drafted
part and [OF-V5](openflow.md#of-v5) is `confirmed`
([Q-OF-11](openflow.md#q-of-11)). Only enqueue reads the
`DISABLE_FLOW_SCRIPT` switch; the worker looks the lite form up first at
pickup and at every transition, so OF-V5's two-phase selection holds, and
OF-3's revision note keeps the reading it replaces. Batch E3f recorded
both selections.

The SC5 revision of 2026-10-30 settled the last four, all by batch E3f's
`oracle/colliding-ids` recordings ([Q-OR-17](oracle.md#q-or-17)), which
kept every colliding pair of steps apart under the typed site paths:
[OR-11](oracle.md#or-11), [OR-14](oracle.md#or-14),
[OR-V3](oracle.md#or-v3) and [OR-V8](oracle.md#or-v8) are now
`confirmed`, and their revision notes keep the readings they replace.

No finding is unsettled at present.

## Changing the reference

1. Only the semantics team edits findings. A build-team question reaches it
   as a request to cover a behaviour, never as a source excerpt.
2. A new finding takes the next free number in its prefix. A correction edits
   the finding in place and keeps its ID.
3. When an oracle recording settles an open question, update the finding's
   confidence qualifier, the provenance of its conformance cases, and the
   question's entry.
