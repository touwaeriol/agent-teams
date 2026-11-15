// Every write the web app makes, as plain async functions over the client.
// Pages wrap them with `useAction` (mutations.ts) for toasts and cache refresh.
import { client, unwrap } from "./client";
import type { components } from "./schema";
import type { MainTaskStatus } from "./views";

type S = components["schemas"];

/** Idempotency-Key for create-style POSTs (api.md §0): one per user action. */
export function idempotencyKey(): string {
  const c = globalThis.crypto;
  return c?.randomUUID ? c.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

const idem = () => ({ headers: { "Idempotency-Key": idempotencyKey() } });

export const auth = {
  login: (body: S["Credentials"]) => unwrap(client.POST("/auth/login", { body })),
  register: (body: S["Credentials"]) => unwrap(client.POST("/auth/register", { body })),
  logout: () => unwrap(client.POST("/auth/logout")),
  changePassword: (body: S["PasswordInputBody"]) => unwrap(client.POST("/api/me/password", { body })),
  revokeSession: (id: string) => unwrap(client.DELETE("/api/me/sessions/{id}", { params: { path: { id } } })),
};

export const projects = {
  create: (body: S["ProjectInput"]) => unwrap(client.POST("/api/projects", { body, ...idem() })),
  update: (pid: string, body: S["ProjectUpdate"]) => unwrap(client.PATCH("/api/projects/{pid}", { params: { path: { pid } }, body })),
  remove: (pid: string) => unwrap(client.DELETE("/api/projects/{pid}", { params: { path: { pid } } })),
  addMemory: (pid: string, text: string) => unwrap(client.POST("/api/projects/{pid}/memory", { params: { path: { pid } }, body: { text } })),
  updateMemory: (pid: string, id: string, body: S["MemoryPatchInputBody"]) =>
    unwrap(client.PATCH("/api/projects/{pid}/memory/{id}", { params: { path: { pid, id } }, body })),
  removeMemory: (pid: string, id: string) => unwrap(client.DELETE("/api/projects/{pid}/memory/{id}", { params: { path: { pid, id } } })),
  requestDeliverable: (pid: string, kind: string, brief: string) =>
    unwrap(client.POST("/api/projects/{pid}/deliverables/request", { params: { path: { pid } }, body: { kind, brief }, ...idem() })),
  addDeliverable: (pid: string, body: { kind: string; title: string; url: string; thread_no?: number }) =>
    unwrap(client.POST("/api/projects/{pid}/deliverables", { params: { path: { pid } }, body, ...idem() })),
  upload: (pid: string, body: S["UploadBody"]) => unwrap(client.POST("/api/projects/{pid}/library", { params: { path: { pid } }, body })),
  /** An image for a message: name + base64 data (PNG, JPEG, GIF or WebP, at most 5 MB). */
  attach: (pid: string, body: S["AttachmentBody"]) => unwrap(client.POST("/api/projects/{pid}/attachments", { params: { path: { pid } }, body })),
  openThread: (pid: string, body: { title: string; brief: string }) =>
    unwrap(client.POST("/api/projects/{pid}/threads", { params: { path: { pid } }, body, ...idem() })),
};

export const deliverables = {
  rename: (id: string, title: string) => unwrap(client.PATCH("/api/deliverables/{id}", { params: { path: { id } }, body: { title } })),
  remove: (id: string) => unwrap(client.DELETE("/api/deliverables/{id}", { params: { path: { id } } })),
  comment: (id: string, text: string, version?: number) =>
    unwrap(client.POST("/api/deliverables/{id}/comments", { params: { path: { id } }, body: { text, version } })),
};

export const schedules = {
  create: (pid: string, body: S["ScheduleBody"]) => unwrap(client.POST("/api/projects/{pid}/schedules", { params: { path: { pid } }, body })),
  update: (id: string, body: S["ScheduleBody"]) => unwrap(client.PATCH("/api/schedules/{id}", { params: { path: { id } }, body })),
  remove: (id: string) => unwrap(client.DELETE("/api/schedules/{id}", { params: { path: { id } } })),
  run: (id: string) => unwrap(client.POST("/api/schedules/{id}/run", { params: { path: { id } } })),
};

export const conversations = {
  send: (cid: string, content: string, interrupt = false, attachments: string[] = []) =>
    unwrap(
      client.POST("/api/conversations/{cid}/messages", {
        params: { path: { cid } },
        body: { content, interrupt, ...(attachments.length ? { attachments } : {}) },
        ...idem(),
      }),
    ),
  interrupt: (cid: string) => unwrap(client.POST("/api/conversations/{cid}/interrupt", { params: { path: { cid } } })),
  answer: (cid: string, qid: string, answer: string, reason?: string) =>
    unwrap(client.POST("/api/conversations/{cid}/questions/{qid}/answer", { params: { path: { cid, qid } }, body: { answer, reason }, ...idem() })),
};

export const threads = {
  update: (tid: string, body: { title?: string; status?: "paused" | "running" | "archived"; auto_approve?: boolean }) =>
    unwrap(client.PATCH("/api/threads/{tid}", { params: { path: { tid } }, body })),
  archive: (tid: string) => unwrap(client.POST("/api/threads/{tid}/archive", { params: { path: { tid } } })),
  unarchive: (tid: string) => unwrap(client.POST("/api/threads/{tid}/unarchive", { params: { path: { tid } } })),
  remove: (tid: string) => unwrap(client.DELETE("/api/threads/{tid}", { params: { path: { tid } } })),
  setMainTask: (tid: string, mid: string, status: MainTaskStatus) =>
    unwrap(client.PATCH("/api/threads/{tid}/main-tasks/{mid}", { params: { path: { tid, mid } }, body: { status } })),
  cancelTask: (tid: string, task: string) => unwrap(client.POST("/api/threads/{tid}/tasks/{task}/cancel", { params: { path: { tid, task } } })),
  retryTask: (tid: string, task: string) => unwrap(client.POST("/api/threads/{tid}/tasks/{task}/retry", { params: { path: { tid, task } } })),
  interruptMember: (tid: string, name: string) =>
    unwrap(client.POST("/api/threads/{tid}/members/{name}/interrupt", { params: { path: { tid, name } } })),
  messageMember: (tid: string, name: string, content: string) =>
    unwrap(client.POST("/api/threads/{tid}/members/{name}/message", { params: { path: { tid, name } }, body: { content }, ...idem() })),
  openPr: (tid: string, force = false) => unwrap(client.POST("/api/threads/{tid}/open-pr", { params: { path: { tid } }, body: { force } })),
  accept: (tid: string) => unwrap(client.POST("/api/threads/{tid}/accept", { params: { path: { tid } } })),
  reject: (tid: string, reason: string) => unwrap(client.POST("/api/threads/{tid}/reject", { params: { path: { tid } }, body: { reason } })),
};

export const nodes = {
  create: (name: string) => unwrap(client.POST("/api/nodes", { body: { name } })),
  update: (id: string, body: S["NodePatchInputBody"]) => unwrap(client.PATCH("/api/nodes/{id}", { params: { path: { id } }, body })),
  rotate: (id: string) => unwrap(client.POST("/api/nodes/{id}/token", { params: { path: { id } } })),
  upgrade: (id: string) => unwrap(client.POST("/api/nodes/{id}/upgrade", { params: { path: { id } } })),
  remove: (id: string) => unwrap(client.DELETE("/api/nodes/{id}", { params: { path: { id } } })),
};

export const roles = {
  save: (list: S["Role"][]) => unwrap(client.PUT("/api/roles", { body: { roles: list } })),
  saveYaml: (yaml: string) => unwrap(client.PUT("/api/roles/yaml", { body: { yaml } })),
  randomLook: (role: string) => unwrap(client.POST("/api/roles/look/random", { body: { role } })),
};

export const skills = {
  /** zip: base64 of a zip file; files: a picked folder's text files. */
  create: (body: S["NewSkillInputBody"]) => unwrap(client.POST("/api/skills", { body })),
  remove: (id: string) => unwrap(client.DELETE("/api/skills/{id}", { params: { path: { id } } })),
};

export const plugins = {
  /** zip: base64 of a plugin zip; the same name again becomes a new version. */
  upload: (zip: string) => unwrap(client.POST("/api/plugins", { body: { zip } })),
  setEnabled: (id: string, enabled: boolean) => unwrap(client.PATCH("/api/plugins/{id}", { params: { path: { id } }, body: { enabled } })),
  remove: (id: string) => unwrap(client.DELETE("/api/plugins/{id}", { params: { path: { id } } })),
};

export const git = {
  addToken: (body: S["NewConnectionInputBody"]) => unwrap(client.POST("/api/git/connections", { body })),
  remove: (id: string) => unwrap(client.DELETE("/api/git/connections/{id}", { params: { path: { id } } })),
  refresh: (id: string) => unwrap(client.POST("/api/git/connections/{id}/refresh", { params: { path: { id } } })),
  createApp: (organization: string) => unwrap(client.POST("/api/admin/github-app/manifest", { body: { organization } })),
  forgetApp: () => unwrap(client.DELETE("/api/admin/github-app")),
};

export const admin = {
  updateUser: (id: string, body: S["UserPatchInputBody"]) => unwrap(client.PATCH("/api/admin/users/{id}", { params: { path: { id } }, body })),
  removeUser: (id: string) => unwrap(client.DELETE("/api/admin/users/{id}", { params: { path: { id } } })),
  resetPassword: (id: string) => unwrap(client.POST("/api/admin/users/{id}/password-reset", { params: { path: { id } } })),
  saveSettings: (body: S["AdminSettings"]) => unwrap(client.PUT("/api/admin/settings", { body })),
  uploadPlugin: (zip: string) => unwrap(client.POST("/api/admin/plugins", { body: { zip } })),
  setPluginEnabled: (id: string, enabled: boolean) => unwrap(client.PATCH("/api/admin/plugins/{id}", { params: { path: { id } }, body: { enabled } })),
  removePlugin: (id: string) => unwrap(client.DELETE("/api/admin/plugins/{id}", { params: { path: { id } } })),
  createSkill: (body: S["NewSkillInputBody"]) => unwrap(client.POST("/api/admin/skills", { body })),
  removeSkill: (id: string) => unwrap(client.DELETE("/api/admin/skills/{id}", { params: { path: { id } } })),
};
