---
name: design
description: Make HTML prototypes, UI mockups, diagrams and visual design documents, then publish them with publish_file. Use when asked for a design, prototype, mockup, landing page, screen, wireframe, diagram or visual explainer.
---

# Design: HTML prototypes and visual design docs

You design by writing files. A design is a **single self-contained HTML file** (a diagram is an
SVG inlined in that HTML) that you write in your working directory and then publish with the platform tool
`publish_file`. People look at it on the project's Deliverables page and their comments come
back to you as messages; answer them by publishing a new version.

Screenshots, images and SVG files are **not deliverables**. `publish_file` on a lone PNG/JPG/GIF/WebP
only saves it as an attachment in the thread's messages (no deliverable is created), and a lone
SVG is refused. To show an image in a deliverable, put it in a directory with the `index.html` that
uses it and publish the directory, or inline it in the HTML.

## 1. Before you start

- Read the brief and the task. If the audience, the screens or the content are unclear, ask
  (`ask_lead`, or `ask_user` when only the user can answer) before drawing.
- Look at the product you are designing for: existing pages, components, colours, wording in
  the repository. Reuse its vocabulary and visual language instead of inventing a new one.
- Decide what the design must show: the screens or states, the key interactions, real-looking
  content (no lorem ipsum; use plausible names, numbers and copy in the product's language).

## 2. Build rules (the hosting enforces some of these)

- **One file.** Put CSS in a `<style>` block and JS in a `<script>` block in the same HTML
  file. Inline small images as SVG or `data:` URIs. A multi-page prototype can be one file with
  in-page navigation (tabs, `#hash` routing); if you really need several files, put them in one
  directory with an `index.html` and publish the directory.
- **No network except these CDNs**, pinned to an exact version:
  `cdn.jsdelivr.net/npm/`, `unpkg.com`, `cdnjs.cloudflare.com`, and Google Fonts. No
  analytics, no APIs, no fetches to the product's backend: the page is shown in a sandboxed
  frame with a unique origin, so cookies, `localStorage` and same-origin requests do not work.
  Keep state in JS variables.
- **Light and dark.** Define colours as CSS custom properties on `:root` and override them in
  `@media (prefers-color-scheme: dark)`. Check contrast in both (text at least 4.5:1).
- **Responsive.** It must work from 360 px phone width to a wide desktop: fluid layouts, no
  horizontal scroll, touch targets of at least 40 px.
- **Accessible.** Semantic elements (`header`, `nav`, `main`, `button`, `label`), alt text,
  visible focus, keyboard reachable controls.
- **Interactive where it matters.** Clicking primary buttons, switching tabs, opening a dialog,
  showing empty / loading / error states: wire them up with a few lines of JS so reviewers can
  try the flow, but do not build a real app.
- Put a short header comment in the HTML: what the design is, which states it shows, open
  questions.

## 3. Check it

- If a browser is available on this machine (for example `npx playwright` or a headless
  Chrome), take screenshots at 390 px and 1280 px wide, in light and dark, and look at them
  (Read the PNG). Fix overlaps, overflow, clipped text and contrast problems. These screenshots
  are for checking your own work, not deliverables; if a reviewer needs one, put it in the report
  or mention it in your task report.
- Without a browser, re-read the HTML carefully: unclosed tags, CSS variables used but not
  defined, scripts that reference missing elements.
- Open the file with a quick HTML validator if one is installed; otherwise at least make sure
  the document has `<!doctype html>`, `<meta charset="utf-8">` and a viewport meta tag.

## 4. Revising an existing design

To change a design that already exists (yours from an earlier session, or one another employee or
thread published), never start a new deliverable:

1. `list_deliverables()` (or `list_deliverables(thread=3, kind="design")`) to find its id.
2. `read_deliverable(deliverable_id="dl_…")` downloads the latest version (or `version=N`) into
   `.agent-deliverables/<id>/v<N>/` in your working directory. That folder is git-ignored, a site
   keeps its sub-folders, and the result also lists the version history and the user's comments.
3. Edit the files in place there.
4. `publish_file(path=".agent-deliverables/<id>/v<N>/index.html", kind="design", title="…",
   deliverable_id="dl_…")` (the folder for a multi-file site): this becomes the next version,
   recorded under your name, and comments on it come to you.

## 5. Publish

Call `publish_file` with the path relative to your working directory:

```
publish_file(path="design/checkout.html", kind="design", title="Checkout flow")
```

- Keep the returned `deliverable_id`. When you change the design (after comments or a new
  brief), publish the same path again with `deliverable_id` set: that creates a new version
  instead of a new deliverable.
- Use `kind="doc"` for a written design document (see the docs skill) and `kind="design"` for
  prototypes, mockups and diagrams.
- In your task report (`complete_task` / message to the lead) include the deliverable id and a
  two-line summary of what the design shows and what is still open.
- Design files do not belong in the product's source tree unless the task says so: keep them
  in a `design/` folder and do not commit them unless asked.
