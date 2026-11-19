# Windmill semantics reference

**Current state:** 352 [findings](../glossary.md#finding) in eight area
files, and a catalogue of 1,107
[conformance cases](../glossary.md#conformance-case). Seven area files are
pinned at Windmill `v1.862.0` and one, licensing.md, is still at the
previous pin, `v1.767.0` ([Pinned version](#pinned-version)). The
[oracle](../glossary.md#oracle) has recorded 715 of the cases. No finding is
unsettled.

This README covers `docs/semantics/`: this index and the area files that it
links. The reference states the behaviour of
[Windmill](../glossary.md#windmill) that windmill-lite must reproduce, as
numbered findings. windmill-lite's code and tests cite these findings.

## What the reference is

- **Windmill's behaviour only.** Each finding states what Windmill does at
  the [pinned version](../glossary.md#pinned-version). How windmill-lite
  builds it is in the [design records](../design/README.md).
- **One rule in each finding,** with an ID, a scope, a confidence level and a
  status ([Finding format](#legend)). Most findings also have conformance
  cases that test them; a finding without a case of its own says so in its
  **Conformance** line.
- **Expected outputs:** each case's provenance tag says where its expected
  output comes from ([conformance.md](conformance.md#how-to-read-a-case)):
  - `oracle-observed`: recorded on the oracle
    ([recording](../glossary.md#recording)).
  - `source-derived`: not recorded. The output usually comes from Windmill's
    pinned source, and sometimes from windmill-ts at its pinned commit, from
    both, or from a rule of windmill-lite or of the oracle harness; the
    case, its finding and the area's notation say which.
  - `policy`: a check of a choice that windmill-lite makes, not of Windmill
    behaviour.
- **Other Windmill versions:** the reference claims nothing about them.
  Where the source and the [public documentation](https://www.windmill.dev/docs)
  disagree, the finding follows the source at the pin and notes the
  disagreement.

## Who writes it and who reads it

The [semantics team](../glossary.md#semantics-team) writes this reference.
It may read Windmill's AGPLv3 source at the pin, and describes behaviour in
its own words: rules, tables, decision lists, input-to-output examples, and
source pointers. No Windmill code is reproduced here.

The [build team](../glossary.md#build-team) (everyone who designs, reviews
or implements windmill-lite) works from this reference, the public
documentation, the Apache-2.0 parts of Windmill and observed behaviour.
**Source pointers are for the semantics team to verify and update findings.
Build-team members do not follow them into AGPL or proprietary files**:
Windmill is AGPLv3 unless a part says otherwise, and
[licensing.md](licensing.md) maps which parts do.
[AGENTS.md](../../AGENTS.md#clean-room) holds the binding contributor rules.

## Pinned version

| | |
|---|---|
| Windmill release | `v1.862.0` |
| Commit | `120415af151593716d39140c8a7d7f3bbe9d99cf` |
| Previous pin | `v1.767.0`, commit `6de4ec0f665ad856002495c4d86269105ec28ad7` |
| Source pointers | `path:line @ <release>`, relative to the root of the [Windmill repository](https://github.com/windmill-labs/windmill/tree/v1.862.0) |

The reference is moving from `v1.767.0` to `v1.862.0`, the release that
windmill-lite follows, one area file at a time. Each area file's header
names the release that it is pinned at:

- **A file still at `v1.767.0`** has not moved yet. All of it, its bare
  source pointers included, is at `v1.767.0`.
- **In a file at `v1.862.0`,** a finding whose tag line ends
  `verified at v1.767.0 only` has not been re-checked against `v1.862.0`
  yet. Its rule and cases were checked at `v1.767.0`, its status refers to
  that check, and its bare pointers are lines of `v1.767.0`. Build on it
  as stated until a re-check at `v1.862.0`.
- **`verified at v1.767.0 only, except …`** names the part that was
  re-checked at `v1.862.0`. A note in the finding says what changed, and
  the pointers of that part say `@ v1.862.0`.
- **A finding without the marker** was re-checked at `v1.862.0` as a whole,
  and its pointers are lines of `v1.862.0`.

The oracle records CE `v1.862.0` since 2026-11-06, and each committed
recording names its release in its provenance. A case whose run has such a
recording holds at `v1.862.0` for what the run's `expect` asserts, whether
or not its finding carries the marker. Recordings and oracle runs made
before that date are of CE `v1.767.0`. A note that depends on the version
names it, such as "recorded on CE `v1.767.0`".

## Areas

| File | What it covers | Finding IDs |
|---|---|---|
| [openflow.md](openflow.md) | The OpenFlow model as Windmill decodes and normalises it, deploy-time validation, the `wmill sync` folder layout, and prior art for parsing it. | `OF-`, `SY-`, `PA-` |
| [execution.md](execution.md) | How a flow advances step by step: `flow_input`, `results`, `previous_result`, transforms, `skip_if`, `stop_after_if`, early return, sleep, timeout, retry, `continue_on_error`, error shapes, failure and preprocessor modules, cancellation. | `FX-` |
| [control-flow.md](control-flow.md) | `forloopflow`, `whileloopflow`, `branchone`, `branchall`, subflows and `identity`; result shapes, failure handling and `results` visibility across container boundaries. | `CF-` |
| [expressions.md](expressions.md) | The JavaScript expression evaluator: engine and limits, fast paths, every expression site and its scope, static values and `$var:`/`$res:` references. | `JE-` |
| [languages.md](languages.md) | How a script runs, by language: Bun in depth (arguments, results, errors, environment, bundling and imports), codebase bundles, the native runtime of `nativets` and `bunnative` steps, and Deno, Python, Go and Bash in summary. | `WR-` |
| [client-api.md](client-api.md) | The `wmill` client surface a script can call, the endpoint behind each call, and the platform services windmill-lite must provide. | `WA-` |
| [oracle.md](oracle.md) | Differential testing against a real Windmill CE `v1.862.0`: topology, bootstrap, running flows, capturing per-step results, normalisation, fixture format, and the observations recorded. | `OR-` |
| [licensing.md](licensing.md) | The licence map of Windmill's repository, what windmill-lite may read, depend on or copy, and the decision on using the CE image as a test oracle. | `LIC-` |
| [conformance.md](conformance.md) | The catalogue of conformance cases of the findings that have cases, grouped by area and tagged by scope: the seed of the shared Go and harness conformance suite. | case IDs |

### Counts

| Area | Findings | `core` | `later` | `out` | `EE` | `drafted` | With a drafted part | Not settled | Not fully re-checked at `v1.862.0` | Question entries | Conformance cases |
|---|---|---|---|---|---|---|---|---|---|---|---|
| [openflow.md](openflow.md) | 52 | 42 | 9 | 1 | 0 | 14 | 2 | 0 | 51 | 13 | 156 |
| [execution.md](execution.md) | 58 | 47 | 9 | 2 | 0 | 0 | 7 | 0 | 54 | 21 | 180 |
| [control-flow.md](control-flow.md) | 37 | 30 | 6 | 0 | 1 | 0 | 11 | 0 | 36 | 19 | 124 |
| [expressions.md](expressions.md) | 57 | 40 | 14 | 3 | 0 | 0 | 18 | 0 | 54 | 17 | 203 |
| [languages.md](languages.md) | 64 | 45 | 17 | 0 | 2 | 3 | 14 | 0 | 62 | 28 | 217 |
| [client-api.md](client-api.md) | 41 | 29 | 10 | 1 | 1 | 0 | 8 | 0 | 41 | 13 | 135 |
| [oracle.md](oracle.md) | 40 | 37 | 2 | 0 | 1 | 0 | 3 | 0 | 39 | 18 | 89 |
| [licensing.md](licensing.md) | 3 | 3 | 0 | 0 | 0 | 0 | 0 | 0 | 3 | 1 | 3 |
| **Total** | **352** | **273** | **67** | **7** | **5** | **17** | **63** | **0** | **340** | **130** | **1107** |

How to read the columns:

- **Findings** are counted by their primary scope tag. The withdrawn stub
  [JE-4](expressions.md#je-4), an ID kept with no rule, is not counted.
- **`drafted`** counts findings whose whole status is `drafted`. They are
  the OpenFlow finding on the script hash format,
  [OF-14](openflow.md#of-14); the sync-folder findings
  [SY-4](openflow.md#sy-4), [SY-5](openflow.md#sy-5),
  [SY-7](openflow.md#sy-7), [SY-8](openflow.md#sy-8),
  [SY-10](openflow.md#sy-10), [SY-11](openflow.md#sy-11),
  [SY-13](openflow.md#sy-13), [SY-15](openflow.md#sy-15) and
  [SY-16](openflow.md#sy-16) to [SY-20](openflow.md#sy-20); and the
  language findings [WR-51](languages.md#wr-51) to
  [WR-53](languages.md#wr-53).
- **With a drafted part** counts `confirmed` findings whose status names a
  part that is still `drafted`, such as [WR-15](languages.md#wr-15).
- **Not settled** counts findings whose status is `DISPUTED`,
  `needs-evidence` or `open` ([Findings that are not settled](#not-settled)).
- **Not fully re-checked at `v1.862.0`** counts the findings whose tag line
  carries the marker `verified at v1.767.0 only` (268 with the marker alone,
  69 with `, except …` naming a part that was re-checked), and the 3
  findings of licensing.md, which is still at `v1.767.0`.
- **Question entries** include answered questions, which keep their
  numbers. The licence question in openflow.md points to licensing.md,
  where it lives as [Q-OF-7](licensing.md#q-of-7).
- **Conformance cases:** 715 are `oracle-observed`, 385 are
  `source-derived`, and 7 are windmill-lite `policy` checks rather than
  Windmill behaviour ([conformance.md](conformance.md#summary)).

<a id="legend"></a>
## Finding format

Each finding starts with an anchor equal to its lower-cased ID
(`execution.md#fx-12`), a heading `### <ID> · <title>`, and one tag line:

```text
**Scope:** … · **Confidence:** … · **Status:** …
```

In a file pinned at `v1.862.0`, the tag line can end with a re-check marker
([Pinned version](#pinned-version)). A finding whose parts differ in scope,
confidence or status gives each part in parentheses, such as
`core (loader rule); later (lock generation)`.

### Scope

| Tag | Meaning |
|---|---|
| `core` | windmill-lite's first release must match this behaviour exactly. |
| `later` | Needed to run general Windmill folders, but not for the first release. |
| `out` | Out of scope: Windmill's scheduling and platform concerns (job queue, worker tags, priorities, concurrency limits, job rows and UI, durable suspend and approval). Where it matters, the finding still says what a flow does when it meets the feature. |
| `EE` | Enterprise Edition behaviour. Out of scope unless it is observable on the Community Edition. |

The primary tag comes first. It is the most demanding tag that applies:
`core` before `later` before `out`.

### Confidence

| Level | Meaning |
|---|---|
| `high` | The code path was read and traced at the pin. |
| `medium` | The code was read but the path was not traced end to end. |
| `low` | Inferred. |

A qualifier in parentheses gives the provenance:

| Qualifier | Meaning |
|---|---|
| `source-traced` | No oracle recording yet. |
| `oracle pending` | An open question asks for a recording. |
| `oracle-observed` | Recorded on a running Windmill CE: `v1.767.0`, or `v1.862.0` for recordings made since 2026-11-06 ([oracle.md](oracle.md)). |

### Status

| Status | Meaning |
|---|---|
| `confirmed` | An independent check against the pinned source confirmed the finding as written. |
| `drafted` | The semantics team wrote the finding from the pinned source; the independent check has not run yet. Build on it as stated; if the check corrects it, the finding is edited in place. |
| `DISPUTED` | An independent check against the pinned source disputed the latest text: the final check of the study the finding comes from, or a later review of this reference (the entry says which). The entry shows the rule as drafted and the correction side by side, and an open question tracks it. Neither side is settled behaviour: do not build on it until the question is closed. |
| `needs-evidence` | The finding lacks the evidence its claim needs. |
| `open` | The behaviour itself is undecided; see the linked open question. |
| `withdrawn`, `merged into …` | The ID is kept so it is never reused; the entry has no rule of its own. |

A status such as `confirmed; drafted (<part>)` names the part that is still
`drafted`.

### The parts of a finding

| Part | What it holds |
|---|---|
| **Rule:** | The behaviour, precise enough to implement and test. |
| **Evidence:** | Source pointers at the pin, and public documentation links. |
| **Conformance:** | Links to the finding's cases in [conformance.md](conformance.md), or says that the finding has none of its own. |
| **Note:** | Cross-references and qualifications. |
| **Design note (not Windmill behaviour):** | A proposal for windmill-lite made while studying the behaviour. It binds nothing; the design decides. |
| **Settled:** | One line: what a re-check or a recording settled, and how, such as "recorded on the oracle as stated" with the fixture and the run, or "read from the pinned source". It keeps any fact that the rule does not state, such as a version boundary or the reading that the settled text replaced, as sub-items where one sentence is not enough. |
| **Not recorded:** | Why the oracle cannot record a case. When a future recording is requested, the line says where the request lives; a case that the oracle can never record, such as a restart that the Community Edition does not offer, has none. |

A finding or case has such a line only where something was settled or
cannot be recorded. A `Settled:` or `Not recorded:` line has no date and no
work history; Git holds the history of each finding. Some area files still
give these notes a dated label, such as `Revision <date> (…)`,
`Correction <date> (…)` or `Not recorded (…)`; such a note says the same
kind of thing.

## Citing findings and cases

A finding's ID is stable: a corrected finding keeps its ID, numbering has
gaps, and a withdrawn ID is never reused. A case ID is the finding's ID and
a letter, such as `FX-12/a`, and is stable in the same way.

| Where | How to cite |
|---|---|
| A Markdown document | A relative link to the finding's anchor, in a sentence that states the rule in plain words ([Identifiers](../writing.md#identifiers)). A case has no anchor of its own: link its ID to its finding's group in conformance.md, such as `[OF-14/f](conformance.md#of-14)` |
| A Go doc comment | A Go doc link such as `[FX-12]`, in the sentence that states the rule, with a link definition that gives the finding's address on GitHub |
| A conformance fixture or case | The bare ID in its `spec` field, such as `spec: [CF-19/a, CF-19]` |

Other ID rules:

- IDs with a `V` (`FX-V3`) were added by an independent check of the draft.
  They are ordinary findings.
- Oracle findings use `OR-` so that they do not collide with the OpenFlow
  `OF-` findings.
- The [glossary](../glossary.md#finding-prefixes) maps each prefix to its
  area.

## Open questions

Each area file ends with its open questions, numbered `Q-<prefix>-<n>` (for
example `Q-FX-15`), with an anchor equal to the lower-cased ID. A question
says what would settle it: usually an oracle recording, sometimes a further
source trace or a human decision. Answered questions keep their number and
point to the finding that answers them.

<a id="not-settled"></a>
### Findings that are not settled

No finding is unsettled at present.

A finding is not settled while its status is `DISPUTED`, `needs-evidence`
or `open`. Do not implement against it until its open question closes. Its
entry shows the drafted rule and the competing claim side by side, and its
cases are tagged `disputed`. When a re-check or a recording settles it, the
finding becomes `confirmed`, and its `Settled:` line keeps the reading that
lost.

## Requests

- **Recording requests.** What only a running Windmill can show is
  requested from the oracle in the design README's
  [recording requests](../design/README.md#recording-requests-for-a-future-batch-e3f-sc5).
  A case that waits on one says so in a `Not recorded:` line, or in the
  open question that tracks it.
- **Requests to the semantics team.** The build team asks for a behaviour
  to be covered in the design README's
  [requests to the semantics team](../design/README.md#requests-to-the-semantics-team).
  An answered request names the findings that answer it; an open request
  says what it still asks.

## Changing the reference

1. Only the semantics team changes what a finding says. A build-team
   question reaches it as a request to cover a behaviour, never as a source
   excerpt.
2. A new finding takes the next free number in its prefix. A correction
   edits the finding in place and keeps its ID.
3. When an oracle recording settles an open question, update the finding's
   confidence qualifier, the provenance of its conformance cases, and the
   question's entry.
4. Record what a re-check or a recording settled in one `Settled:` line, or
   why a case cannot be recorded in one `Not recorded:` line
   ([The parts of a finding](#the-parts-of-a-finding)).
