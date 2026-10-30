# Writing standard

This standard says how to write windmill-lite's documentation. It is for
everyone who writes or reviews it: people and coding agents. Memoria lists
this file in every documentation review, and CI reports the rules that it
can count.

## The rule

Write what is true and what the reader needs, in a structure that the reader
can scan. Leave out everything else.

- **Precise:** each statement is exactly true of the code today. Name the
  value, the unit, the file or the command: "times out after 30 s", not
  "times out quickly". Say what is a stub and what waits on later work.
- **Useful:** each statement answers a question that the document's reader
  has. Leave out process history, review notes, and context that the reader
  already has.
- **Structured:** a reader finds a fact by scanning headings, lists and
  tables, without reading every paragraph.

Short text is a result of these rules, not the goal. Add a sentence when the
reader needs it.

## Structure

Structure is the main rule. Fix the structure of a document before you edit
its sentences.

- **Headings that a reader can scan.** A heading names its topic in a few
  words, such as "Run a flow" or "Environment variables".
- **One topic in each section.** When a section covers two topics, split it.
- **The most important fact first.** Put it first in the document, in each
  section and in each paragraph. Details, exceptions and reasons come after
  it.
- **The right form for the content.** The table below gives the form for
  each kind of content.
- **Short blocks.** A block of prose has at most 4 sentences and at most 80
  words. A block is a paragraph, a list item or a table cell. Split a longer
  block, or turn it into a list or a table.
- **Links instead of repetition.** Each fact has one home, and other
  documents link to it. To show the same text in two documents, use a
  Memoria import ([Memoria skill](../.claude/skills/memoria/SKILL.md#markers)).
- **Examples where they help.** When a rule is easier to see than to state,
  show an input and its output.

| Content | Form |
|---|---|
| A procedure | Numbered steps, with one action in each step |
| A set of items in no order | A bullet list |
| A comparison, or reference data such as options, fields or environment variables | A table |
| A reason, a trade-off, or a cause and its effect | A short paragraph |
| A command, a path, a value or code | A code span or a fenced code block, byte-exact |

## Sentences

These rules adapt principles of ASD-STE100 Simplified Technical English
([Sources](#sources)).

- **One idea in each sentence.** Aim for 25 words or fewer. Split a sentence
  of more than 40 words.
- **One action in each instruction.** Write instructions in the imperative:
  "Run `memoria check`." When a step has two actions, make two steps.
- **Active voice.** Name the actor: "the engine retries the step", not "the
  step is retried".
- **Present tense** for what the code does today. Use the future tense only
  for planned work, and say that it is planned.
- **Keep the articles** (a, an, the): "start the harness", not "start
  harness".
- **No long noun clusters.** Break a cluster of more than three nouns with a
  preposition or a verb: "the digest of each file in the manifest", not "the
  manifest file entry digest".
- **Values with their units:** `30 s`, `1 MiB`, `Bun 1.3.10`. Do not write
  "fast", "small", "recent" or "many" when you can give the number. Quote a
  number from the source, or leave it out.
- **No marketing language.** Do not use superlatives, and do not describe
  planned work as if it ships today.

## Words

- **One meaning for each word, and one word for each meaning.** When the
  [glossary](glossary.md) has a term, use that term, and only in that
  meaning. Do not change words for variety: a paragraph that says
  "invocation" does not also say "execution" for the same thing.
- **Explain each term at its first use** in a document, or link it to the
  glossary.
- **Add a term to the glossary** when two or more documents use it.
- **Spell out an abbreviation** at its first use in a document, unless the
  glossary defines it.

## Document types

Each document type has one reader and one purpose. Write for that reader.

The root README, the package READMEs, the guides and the glossary are
**user-facing**.
They carry no milestone IDs, process history or review notes. That history
belongs in Git, in pull requests and in ADRs.

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
  and what does not, followed by the README's scope; the package's entry
  points; links to the ADRs and findings that it implements.
- Describe only the README's own directory. Link to other READMEs instead of
  copying from them.

### ADR

An ADR (architecture decision record) is a numbered file in `docs/design/`.

- **Reader:** a reviewer or contributor who needs to know why the design is
  what it is.
- **Purpose:** record one decision: its context, the options, the decision
  and its consequences.
- **Contents:** the status and the topic at the top. When code departs from
  an ADR, amend the ADR in the same pull request.
- An ADR is a record of history, so it can cite milestone IDs.

### Semantics reference

- **Reader:** the build team, which implements Windmill's behaviour from it.
- **Purpose:** state Windmill's behaviour as numbered findings that code and
  tests cite.
- **Contents:** one rule in each finding, with its ID, scope, confidence,
  status and conformance cases. [docs/semantics/README.md](semantics/README.md)
  gives the format.
- The semantics team writes it, under the clean-room rules in
  [AGENTS.md](../AGENTS.md#clean-room).

### Conformance cases

- **Reader:** the Go and harness tests that run them, and the contributor
  who reads a failing case.
- **Purpose:** state one input and its expected output.
- **Contents:** the IDs of the findings that the case checks, and a title
  that says what the case shows, where the format has one.

### Code comments

- **Reader:** a maintainer who reads the code.
- **Purpose:** say why the code is as it is, and what the code cannot show.
- **Contents:** a Go doc comment on each exported name, starting with the
  name. Where code implements a finding, cite the finding's ID.
- Do not restate what the code says. Cite a finding or an ADR, not a
  milestone ID.

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
- **Contents:** numbered procedures, with the commands to run.

### Glossary

- **Reader:** anyone who meets a project term and needs its meaning.
- **Contents:** one row for each term: its meaning in one or two sentences,
  a link to the document that defines it, and the words not to use for it.

## Markdown

- Use GitHub-flavoured Markdown.
- Use relative links between files in the repository.
- Draw diagrams in Mermaid, with neutral wording and the default rendering:
  no theme or colour overrides. Each diagram makes one claim.
- Keep code spans, code blocks, paths, identifiers and quoted errors
  byte-exact.

## Example

This paragraph is from the quick start of the root [README](../README.md):

> The harness, build, skeleton and host-mode tests also need Bun 1.3.10, the
> version Windmill v1.767.0 ships; without it they skip. Point `WL_BUN` at
> its executable, and install the build helper's parser once. The Q-WA-4
> test and the facade's attribution test also need the stock
> `windmill-client@1.767.0`: point `WL_WINDMILL_CLIENT` at a directory whose
> `node_modules` holds it (CI fetches it by its npm integrity hash):

It puts a procedure and reference data in one block. Rewritten to this
standard:

> Some tests need Bun 1.3.10 and the stock Windmill client. Without them,
> those tests skip.
>
> | Variable | Value | Tests that need it |
> |---|---|---|
> | `WL_BUN` | The path of a Bun 1.3.10 executable, the Bun of Windmill v1.767.0 | The harness, build, skeleton and host-mode tests |
> | `WL_WINDMILL_CLIENT` | A directory whose `node_modules` holds `windmill-client@1.767.0` | The Q-WA-4 test and the facade's attribution test |
>
> To run these tests:
>
> 1. Install the parser of the build helper once:
>    `(cd harnesses/bun/build && bun install --frozen-lockfile)`.
> 2. Set the two variables.
> 3. Run `go test -race ./...`.

What changed:

- The first sentence gives the most important fact: without two tools, some
  tests skip.
- The two variables are reference data, so they are a table.
- The procedure is numbered steps, with one action in each step.
- The longest sentence has 10 words instead of 30.
- How CI fetches the client is gone. A reader who runs the tests does not
  need it, and the CI workflow is its home.

## What CI checks

[scripts/check-docs.py](../scripts/check-docs.py) reports three rules. CI
runs it on the Markdown that a pull request changes, and reports each
finding as a warning on the changed block. It never fails CI.

| Rule | What it reports |
|---|---|
| `paragraph-length` | A block of more than 4 sentences or more than 80 words |
| `sentence-length` | A sentence of more than 40 words |
| `current-state` | A README with no `**Current state:**` line before its first `##` heading |

To check your own files, run `python3 scripts/check-docs.py <file>...`.
Reviewers check the other rules. [memoria.toml](../memoria.toml) lists them
for every Memoria review.

## Sources

The sentence and word rules adapt principles of ASD-STE100 Simplified
Technical English, a specification for technical writing that ASD
publishes. This standard is written in its own words. It does not reproduce text or
dictionary entries from that specification, which is published under its
own terms.
