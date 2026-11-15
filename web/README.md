# agent-teams web (v2)

React 18 + Vite 7 + Tailwind 3 + Radix/shadcn-style components, react-query 5. Screens follow the
v2 design canvas; routes are `docs/v2/api.md` §4.

```
npm ci
npm run dev:mock        # mock API (mock/server.mjs, port 3001) + vite; log in with any email/password
API_TARGET=http://127.0.0.1:3001 npm run dev   # vite against a real atctl
npm test                # vitest: src/__tests__ (app logic) + src/office/__tests__
npm run build           # tsc -b && vite build → dist/ (the Go server serves it from WEB_DIR)
npm run gen:api         # src/api/openapi.json → src/api/schema.d.ts (after `atctl openapi > src/api/openapi.json`)
```

## Layout

| Path | What |
| --- | --- |
| `src/api/client.ts` | the one openapi-fetch client: same-origin cookie, `X-CSRF-Token` from the `agent_teams_csrf` cookie on writes, `{detail, code, fields}` → `ApiError` |
| `src/api/schema.d.ts` | generated from `openapi.json` (core endpoints) |
| `src/api/orchestration.ts` | orchestration endpoints of api.md written in the generated shape until `atctl openapi` exports them — delete entries as they land |
| `src/api/views.ts` | view types: generated ones re-exported, orchestration ones from api.md §2 |
| `src/api/queries.ts` · `actions.ts` · `mutations.ts` | query keys + hooks, every write, `useAction` (toast + invalidate) |
| `src/api/events.ts` · `sse.ts` | SSE frames written into the query cache; `EventSource` with resume (`Last-Event-ID` on browser reconnects, `?last_event_id=` when we reopen) |
| `src/components/shell` | project sidebar, settings nav, desktop sidebar / phone drawer |
| `src/components/app` | v2 building blocks: pills, checklist marks, composer, cards (approval / question / PR acceptance), chat, thread panel, avatar (drawn with the office renderer), repo fields |
| `src/lib/present.ts` | states → colours and words, list grouping, progress (pure, tested) |
| `src/pages` | Main, Overview, Thread (+ MobileThread), Deliverables, DesignViewer, NewProject, ProjectSettings, Office, Legacy, settings/* |
| `src/office` | the office renderer (separate module, see its README) |
| `mock/` | dev-only mock API with the design's demo data; not part of the build |
