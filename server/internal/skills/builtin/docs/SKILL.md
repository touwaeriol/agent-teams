---
name: docs
description: Write Markdown documents - specs, design docs, plans, reports, READMEs, meeting notes - and publish them with publish_file. Use when asked for a document, spec, proposal, write-up, plan or report.
---

# Docs: Markdown documents and specs

You write documents as Markdown files in your working directory and publish them with the
platform tool `publish_file`. People read them on the Deliverables page and their comments
come back to you as messages; answer by publishing a new version.

## 1. Plan the document

- Who reads it and what decision or action should it enable? Write that down as the first
  paragraph ("This spec describes … so that … can …").
- Gather facts first: read the code, existing docs, issues and the brief. Quote file paths and
  names exactly. Do not invent numbers; mark unknowns as open questions.
- Pick a structure that fits:
  - **Spec**: goal, non-goals, background, design (data, API, UI, flows), alternatives
    considered, risks, rollout, open questions.
  - **Plan**: outcome, milestones with owners and dates, dependencies, risks.
  - **Report**: summary first (3–5 bullet points), then findings with evidence, then
    recommendations.

## 2. Write

- Headings (`#`, `##`, `###`) that a reader can skim; short paragraphs; lists for parallel
  items; tables for comparisons; fenced code blocks with a language tag.
- Lead with the conclusion. Each section answers one question.
- Use the product's language and terminology (write in Chinese if the brief is in Chinese).
- Diagrams: a Mermaid block (```` ```mermaid ````) or a separate SVG published with the
  design skill. Keep diagrams small and labelled.
- Links: relative links to repository files or absolute https URLs. No HTML in Markdown: it
  is not rendered.
- Keep it as short as the content allows. Remove filler sentences.

## 3. Check

- Re-read top to bottom as the target reader. Every claim has a source or is marked as an
  assumption. Every open question has an owner.
- Check that the Markdown renders: balanced code fences, tables with a header separator row,
  no broken list indentation.

## 4. Publish

```
publish_file(path="docs/search-spec.md", kind="doc", title="Search spec")
```

- Keep the returned `deliverable_id`; publish the same path again with `deliverable_id` to
  release a new version after comments.
- Mention the deliverable id and a two-line summary in your task report.
- Documents that belong in the repository (README, docs/ the project maintains) are also
  committed as part of your task; ad-hoc reports and proposals are only published.
