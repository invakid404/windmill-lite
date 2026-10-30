# Writing standard

This standard says how to write windmill-lite's documentation. It is for
everyone who writes or reviews it: people and coding agents.
[Memoria](glossary.md#memoria), the tool that ties each README to the files
that it describes, lists this file in every documentation review. Continuous
integration (CI) reports the rules that a script can count
([What CI checks](#what-ci-checks)).

## The rule

Write what is true and what the reader needs, in a structure that the reader
can scan. Leave out everything else.

- **Precise:** each statement is exactly true of its source of truth (the
  table below). Name the value, the unit, the file or the command: "times
  out after 30 s", not "times out quickly". Say what is a stub, a
  placeholder that does not do the real work yet, and what waits on later
  work.
- **Useful:** each statement answers a question that the document's reader
  has. Leave out process history, review notes, and context that the reader
  already has.
- **Structured:** a reader finds a fact by scanning headings, lists and
  tables, without reading every paragraph.

Short text is a result of these rules, not the goal. Add a sentence when the
reader needs it.

| Document | A statement is true when it matches |
|---|---|
| A README, a guide or a code comment | The code and the tests in the repository today |
| The [semantics reference](glossary.md#semantics-reference) | Windmill's behaviour at the [pinned version](glossary.md#pinned-version) |
| An [ADR](glossary.md#adr) | The accepted decision. Mark what the code does not do yet as planned. |
| A [conformance case](glossary.md#conformance-case) | The [oracle](glossary.md#oracle)'s [recording](glossary.md#recording), or the [finding](glossary.md#finding) that the case names |

## Assumed knowledge

The documentation assumes general software knowledge, and explains
everything that is specific to windmill-lite or to Windmill. A reader is
expected to know:

- Git and GitHub: commits, branches, pull requests, squash merges and force
  pushes;
- CI with GitHub Actions: workflows, jobs, annotations and job summaries;
- Go: packages, modules, exported names and doc comments;
- JavaScript and TypeScript, Node.js, and the CommonJS and ES module
  formats;
- Markdown, JSON, YAML and OpenAPI;
- containers and container images;
- the common licences (AGPLv3, Apache-2.0, MIT) and units (s, ms, MiB).

Explain or link everything else at its first use in a document, such as
[Bun](glossary.md#bun), [Windmill](glossary.md#windmill) and every other
term in the [glossary](glossary.md).

## Self-contained and durable

Every sentence must make sense to a reader who has never seen the project's
plans. It must also stay true after the work that produced it is forgotten.
These rules apply to every document type.

- **Self-contained.** Do not use internal plan or work codes, as names or as
  explanations. These codes are milestone identifiers (IDs) such as `F3b` or
  `SC5`, the numbers of the plan's waves, plan items, review rounds, and pull
  request numbers. Name the thing itself, such as
  [host mode](glossary.md#host-mode), not the work that built it.
- **Durable.** Record what the system does, why it does it, and its limits.
  Do not record the history of the work: what a milestone added or fixed,
  revision notes, or the count of review rounds. Git and pull requests hold
  that history.

### Identifiers

Outside the document that defines it, an identifier, such as a finding ID,
may appear only as a link to its definition, and the same sentence must
state the behaviour or rule in plain words. The ID is a pointer for
traceability, never the explanation, so a reader who does not follow the
link still understands the sentence. Inside the defining document, IDs are
anchors as usual.

The rule covers the prose of documents. It does not cover:

- machine-readable data, such as the field of a conformance case that names
  its finding, which keeps the bare ID that its reader parses;
- code identifiers, file names and wire-format names, which stay byte-exact
  in code spans.

A code comment cannot hold a link, so it writes the ID with the path of the
file that defines it, and states the rule in the same comment. A milestone
ID is not an identifier in this sense: it names work, so the self-contained
rule forbids it.

This sentence is from the README of `internal/platform`:

> The others are windmill-lite's placeholders, not Windmill's, so a
> comparison with the oracle masks them, as OR-29 has the harness do.

The finding's ID is the only explanation of why the comparison masks them.
Rewritten to this rule:

> The others are windmill-lite's placeholders, not Windmill's. A comparison
> with the oracle masks them, as the oracle harness masks values that depend
> on how the oracle runs, such as `WM_USERNAME`
> ([OR-29](semantics/oracle.md#or-29)).

### Work status in ADRs

- An ADR keeps the decision, its context, the alternatives and the
  consequences.
- An ADR does not track the status of work, such as which milestones are
  done.
- When a decision changes, amend the ADR so that it states the current
  decision, and keep the replaced option among the alternatives.

## Structure

Fix the structure of a document first, then edit its sentences. Structure
never overrides precision.

- **Headings that a reader can scan.** A heading identifies its section
  without the body, such as "Run a flow" or "Environment variables".
- **One topic in each section.** The direct body of a section covers one
  topic. A parent section can group related subsections, as
  [Document types](#document-types) does.
- **The most important fact first.** Put the main fact first in the
  document, in each section and in each paragraph, with any limit that it
  needs to be true. Supporting detail and reasons come after it.
- **The right form for the content.** The table below gives the form for
  each kind of content.
- **Short blocks.** A block of prose has at most 4 sentences and at most 80
  words. A block is a paragraph, a list item with all its paragraphs, or a
  table cell; inline code counts as one word, and a link counts by its text.
  Split a longer block, or turn it into a list or a table.
- **Links instead of repetition.** Each fact has one authoritative home, and
  other documents link to it. A short summary next to the link is fine, as
  in the glossary or a checklist. To show the same text in two documents,
  use a Memoria import ([Memoria skill](../.claude/skills/memoria/SKILL.md#markers)).
- **Examples where they help.** When a rule is easier to see than to state,
  show an input and its output.

| Content | Form |
|---|---|
| A procedure | Numbered steps, with one action in each step |
| A set of items in no order | A bullet list |
| A comparison, or reference data such as options, fields or environment variables | A table |
| A reason, a trade-off, or a cause and its effect | A short paragraph |
| A command, a path, a value or code | In backticks, as a code span such as `go test` or a fenced code block, with the same bytes as the source |

## Sentences

These rules adapt principles of ASD-STE100 Simplified Technical English
([Sources](#sources)).

- **One idea in each sentence.** Aim for 25 words or fewer. Split a sentence
  of more than 40 words.
- **One action in each instruction.** Write instructions in the imperative:
  "Run `memoria check`." When a step has two actions, make two steps.
- **Active voice.** Name the actor: "the engine retries the step", not "the
  step is retried".
- **Present tense** for what the system does. Use the future tense only for
  planned work, and say that it is planned.
- **Keep the articles** (a, an, the): "start the harness", not "start
  harness".
- **No long noun clusters.** Break a cluster of more than three nouns with a
  preposition or a verb: "the timeout of each retry of a step", not "the
  step retry timeout value".
- **Quantities and versions.** Give a quantity with its unit, such as 30 s
  or 1 MiB, and a version exactly, such as Bun 1.3.10. Do not write "fast",
  "small", "recent" or "many" when you can give the number. Take each number
  from the code, a test or a linked measurement, or leave it out.
- **No marketing language.** Do not use superlatives, and do not describe
  planned work as if it ships today.

## Words

- **One meaning for each word, and one word for each meaning.** When you
  write about a concept that the [glossary](glossary.md) defines, use its
  term, and only in that meaning. A qualified ordinary use is fine, such as
  a CI job or the repository root. Do not change words for variety: a
  paragraph that says "[invocation](glossary.md#invocation)" does not also
  say "execution" for the same thing.
- **Explain each term at its first use** in a document, or link it to the
  glossary. [Assumed knowledge](#assumed-knowledge) lists what needs no
  explanation.
- **Add a term to the glossary** when two or more documents use it, and it
  is specific to windmill-lite or Windmill, or means something different
  here from its common use.
- **Spell out an abbreviation** at its first use in a document, unless the
  glossary defines it or it is assumed knowledge.

## Document types

Each document type has one reader and one purpose. Write for that reader.
Every type follows [Self-contained and durable](#self-contained-and-durable),
and review notes belong in pull requests.

### Root README

- **Reader:** a person who decides whether to use windmill-lite.
- **Purpose:** say what windmill-lite is, which problem it solves, what
  works today, and how to start.
- **Contents, in this order:** the product and the problem; a
  `**Current state:**` paragraph; a quick start; an index of every module
  README and of `docs/`.

### Package README

- **Reader:** a contributor who changes the package, or a developer who
  calls it.
- **Purpose:** say what the package does today, and how to use and test it.
- **Contents:** a `**Current state:**` line at the top that says what works
  and what does not, followed by the README's scope, the directory that it
  covers. Then the functions, types or commands that a caller starts with,
  and links to the ADRs and findings that the package implements.
- Describe only the README's own directory. Link to other READMEs instead of
  copying from them.

### ADR

- **Reader:** a reviewer or contributor who needs to know why the design is
  what it is.
- **Purpose:** record one decision: its context, the options, the decision
  and its consequences.
- **Contents:** the status and the topic at the top, then the context, the
  decision, the alternatives and the consequences
  ([Work status in ADRs](#work-status-in-adrs)).
- When code departs from an ADR, amend the ADR in the same pull request.

### Semantics reference

- **Reader:** the [build team](glossary.md#build-team), which implements
  Windmill's behaviour from it.
- **Purpose:** state Windmill's behaviour as numbered
  [findings](glossary.md#finding) that code and tests cite.
- **Contents:** one rule in each finding, with its ID, scope, confidence,
  status and conformance cases. [docs/semantics/README.md](semantics/README.md)
  gives the format.
- A finding states Windmill's behaviour at the pinned version. Its history
  is in Git, not in revision notes.
- The [semantics team](glossary.md#semantics-team) writes it, under the
  clean-room rules in [AGENTS.md](../AGENTS.md#clean-room).

### Conformance cases

- **Reader:** the Go and [harness](glossary.md#harness) tests that run them,
  and the contributor who reads a failing case.
- **Purpose:** state inputs and the outputs that windmill-lite must
  produce.
- **Contents:** in its data fields, the IDs of the findings that the case
  checks, and a title that says what the case shows, where the format has
  one.

### Code comments

- **Reader:** a maintainer who reads the code.
- **Purpose:** say why the code is as it is, and what the code cannot show,
  such as a compatibility constraint, an invariant, or the reason for a
  surprising choice.
- **Contents:** a Go doc comment on each exported name, starting with the
  name. Where code implements a finding, write its ID with the path of the
  file that defines it ([Identifiers](#identifiers)).
- Do not restate what the code says. Do not name the milestone or the pull
  request that wrote the code.

### Agent instructions

[AGENTS.md](../AGENTS.md) holds the contributor rules, and `CLAUDE.md`
points to it.

- **Reader:** people and coding agents who contribute.
- **Purpose:** state the rules that every contributor follows.
- **Contents:** rules in the imperative, each of which a reviewer can check.
  Link to the document that explains a rule instead of explaining it again.

### Guides

A guide explains one task or one standard, such as
[docs/memoria.md](memoria.md) or this file.

- **Reader:** a contributor who does that task.
- **Contents:** numbered steps for each procedure that the guide holds, with
  the commands to run.

### Glossary

- **Reader:** anyone who meets a project term and needs its meaning.
- **Contents:** one entry for each term: a heading, its meaning in one or
  two sentences with a link to the document that defines it, and the words
  not to use for it.

## Markdown

- Use [GitHub-flavoured Markdown](https://github.github.com/gfm/).
- Use relative links between files in the repository.
- Draw diagrams in [Mermaid](https://mermaid.js.org), a text format for
  diagrams, with the default rendering: no theme or colour overrides.
- Label the boxes and arrows of a diagram with the names of things and
  actions, not with judgements such as "fast" or "legacy". Each diagram
  answers one question, such as which process starts which.
- Keep code spans, code blocks, paths, identifiers and quoted errors
  byte-exact: with the same bytes as their source.

## Examples

### Structure

This paragraph is from the quick start of the root [README](../README.md):

> The harness, build, skeleton and host-mode tests also need Bun 1.3.10, the
> version Windmill v1.767.0 ships; without it they skip. Point `WL_BUN` at
> its executable, and install the build helper's parser once. The Q-WA-4
> test and the facade's attribution test also need the stock
> `windmill-client@1.767.0`: point `WL_WINDMILL_CLIENT` at a directory whose
> `node_modules` holds it (CI fetches it by its npm integrity hash):

It puts a procedure and reference data in one block, and it names tests by
internal labels. Rewritten to this standard:

> Some tests need Bun 1.3.10, the Bun of Windmill v1.767.0, and the
> unmodified Windmill client library. Each of these tests skips when the
> variable that it needs is not set.
>
> | Variable | Value | Tests that need it |
> |---|---|---|
> | `WL_BUN` | The path of a Bun 1.3.10 executable | Tests in `internal/harness`, `internal/bundle`, `internal/engine`, `internal/platform` and `cmd/windmill-lite` |
> | `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.767.0` | The test that concurrent invocations each see their own `WM_*` environment variables on Bun 1.3.10 ([Q-WA-4](semantics/client-api.md#q-wa-4)), and the test that the [platform facade](../internal/platform/facade/README.md) attributes each call to the step that made it |
>
> To run these tests:
>
> 1. Install the dependencies of the
>    [Bun build helper](../harnesses/bun/build/README.md) once:
>    `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
> 2. Export the two variables in the shell that runs the tests.
> 3. Run `go test -race ./...`.

What changed:

- The opening says first that some tests need two tools, and when they
  skip.
- The two variables are reference data, so they are a table.
- The procedure is numbered steps, with one action in each step.
- Each test is named by what it checks. The one finding ID is a link to its
  definition, next to the plain statement.
- No sentence outside the table has more than 16 words. Before, the longest
  sentence had 30.
- How CI fetches the client is gone. A reader who runs the tests does not
  need it, and the CI workflow is its home.

### Work codes and history

This sentence is from the current state of the root README, shown here
without its links:

```text
F4 gives the `windmill-lite` command its first work, in internal/bundle:
`build` turns a sync folder and a host contract into a bundle (the compiled
program, the bundled Bun scripts and the folder's data) with a manifest that
records the digest of every file and of every input, and whose own digest
identifies the bundle; `verify` checks a bundle against a release manifest;
`selfcheck` starts the expression engine.
```

It names a milestone, and it records history: "its first work". Rewritten
to this standard:

> The `windmill-lite` command ([cmd/windmill-lite](../cmd/windmill-lite/README.md))
> has these subcommands, among others:
>
> - `build` turns a [sync folder](glossary.md#sync-folder) and a
>   [host contract](glossary.md#host-contract) into a
>   [bundle](glossary.md#bundle). The bundle's manifest records the digest,
>   a hash, of every file and of every input, and the bundle's own digest
>   identifies it.
> - `verify` recomputes those digests, and checks them against the bundle's
>   manifest or against a release manifest that you give with `-manifest`.
> - `selfcheck` starts the expression engine, the JavaScript engine that
>   evaluates flow expressions.

What changed:

- The milestone ID and the history are gone. The reader learns what the
  command does today, not which work added it.
- The three subcommands are a list, with one item for each.
- Each project term links to the glossary.

## What CI checks

[scripts/check-docs.py](../scripts/check-docs.py) reports four rules. CI
runs it on the Markdown that a pull request changes, and shows a warning on
each changed block that breaks a rule. A warning never fails the job. An
error in the script itself, such as a file that it cannot read, does fail
it.

| Rule | What it reports |
|---|---|
| `paragraph-length` | A block of more than 4 sentences or more than 80 words |
| `sentence-length` | A sentence of more than 40 words |
| `current-state` | A README with no `**Current state:**` line before its first `##` heading |
| `work-code` | A block or a heading that names a milestone ID, a plan item, a wave number, a numbered review round or a pull request number |

The script counts blocks and words as [Structure](#structure) says. It has
these limits:

- Its sentence count is a heuristic. A sentence ends at a full stop, a
  question mark or an exclamation mark that a space follows, except after a
  common abbreviation such as "e.g.".
- Inline code is not prose, so it does not report a work code in backticks.
  Reviewers check that.
- The `work-code` rule can report a defined name that looks like a milestone
  ID, such as a [fixture](glossary.md#fixture) called `B2`. Keep a defined
  name, and link it to its
  definition. Replace a name that refers to work with the name of the
  behaviour.

To check your own files, run `python3 scripts/check-docs.py <file>...`.
Reviewers check the other rules, which [memoria.toml](../memoria.toml) lists
for every Memoria review.

## Sources

The sentence and word rules adapt principles of
[ASD-STE100 Simplified Technical English](https://www.asd-ste100.org), a
specification for technical writing that ASD, the European association of
the aerospace and defence industries, publishes. This standard is written
in its own words. It does not reproduce text or dictionary entries from that
specification, which is published under its own terms.
