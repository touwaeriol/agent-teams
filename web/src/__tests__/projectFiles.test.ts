// @vitest-environment jsdom
// Project files (the project's folder in the user's shared folder): sizes, path rules mirrored from
// the server, breadcrumbs, the upload plan, batching, folders, and the page: unavailable state,
// browsing, upload with confirmation, new folder, delete.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import type { FileEntry, ProjectFiles as Listing } from "@/api/views";
import {
  BATCH_BYTES,
  breadcrumbs,
  collectFiles,
  downloadUrl,
  droppedFiles,
  filterEntries,
  folderLinkText,
  isManaged,
  joinPath,
  parentDir,
  pathProblem,
  planUpload,
  uploadBatches,
  UploadBatchError,
  uploadErrorText,
  uploadProjectFiles,
} from "@/lib/projectFiles";
import { ApiError } from "@/api/client";
import { formatBytes } from "@/lib/bytes";
import { UploadCancelled } from "@/lib/upload";
import ProjectFiles from "@/pages/ProjectFiles.vue";
import { toast } from "@/lib/toast";

const MB = 1 << 20;
const entry = (path: string, extra: Partial<FileEntry> = {}): FileEntry => ({
  name: path.split("/").pop()!,
  path,
  dir: false,
  size: 10,
  content_type: "text/plain",
  mod_time: "2026-11-26T10:00:00Z",
  ...extra,
});
const folder = (path: string) => entry(path, { dir: true, size: 0, content_type: "" });
const listing = (entries: FileEntry[], extra: Partial<Listing> = {}): Listing => ({ available: true, dir: "", entries, ...extra });
/** A File of the given size without allocating it. */
const fake = (name: string, size: number) => {
  const f = new File(["x"], name);
  Object.defineProperty(f, "size", { value: size });
  return f;
};

describe("paths", () => {
  it("prints binary units", () => {
    expect(formatBytes(820)).toBe("820 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(5 * 1024 ** 4)).toBe("5 TB");
    expect(formatBytes(undefined)).toBe("—");
  });
  it("accepts what the server accepts (hidden names, non-ASCII, leading or trailing /)", () => {
    for (const p of ["a.txt", "data/raw/x.csv", "报告/v2.pdf", ".env", "/docs/"]) expect(pathProblem(p)).toBeNull();
  });
  it("refuses what the server refuses, with a reason", () => {
    expect(pathProblem("")).toBe("路径是空的");
    expect(pathProblem("a//b")).toContain("空的目录名");
    expect(pathProblem("a/../b")).toContain(". 或 ..");
    expect(pathProblem("a\\b")).toContain("反斜杠");
    expect(pathProblem("a\nb")).toContain("控制字符");
    expect(pathProblem("x".repeat(1025))).toContain("1024");
  });
  it("joins, climbs and lists the breadcrumb", () => {
    expect(joinPath("", "a")).toBe("a");
    expect(joinPath("d/", "a")).toBe("d/a");
    expect(parentDir("a/b/c")).toBe("a/b");
    expect(parentDir("a")).toBe("");
    expect(breadcrumbs("data/raw")).toEqual([{ name: "项目文件", path: "" }, { name: "data", path: "data" }, { name: "raw", path: "data/raw" }]);
  });
  it("filters by name and builds an encoded download link", () => {
    expect(filterEntries([entry("a.csv"), entry("notes.md")], "NOTE").map((e) => e.name)).toEqual(["notes.md"]);
    expect(downloadUrl("p1", "data/a b.csv")).toBe("/api/projects/p1/files/download?path=data%2Fa%20b.csv");
  });
  it("keeps the platform's .deliverables folder at the root, and words the setup link by the volume's state", () => {
    expect(isManaged(folder(".deliverables"))).toBe(true);
    expect(isManaged(folder("data/.deliverables"))).toBe(false);
    expect(isManaged(entry(".deliverables"))).toBe(false);
    const vol = (state: string) => ({ configured: true, volume: { state } as never });
    expect(folderLinkText(undefined)).toBe("正在开通，去查看");
    expect(folderLinkText({ configured: true })).toBe("正在开通，去查看");
    expect(folderLinkText({ configured: false })).toBe("去查看");
    expect(folderLinkText(vol("error"))).toContain("重试");
    expect(folderLinkText(vol("provisioning"))).toContain("正在开通");
  });
});

describe("planUpload", () => {
  it("lists the files of the current folder it would replace", () => {
    const plan = planUpload([{ file: fake("a.csv", 4), path: "a.csv" }, { file: fake("b.csv", 4), path: "b.csv" }], [entry("a.csv"), folder("b.csv")]);
    expect(plan.overwrites).toEqual(["a.csv"]);
    expect(plan.problems).toEqual([]);
  });
  it("warns once when an uploaded folder writes into an existing folder", () => {
    const plan = planUpload([{ file: fake("a.md", 1), path: "docs/a.md" }, { file: fake("b.md", 1), path: "docs/sub/b.md" }, { file: fake("c", 1), path: "new/c" }], [folder("docs"), entry("new")]);
    expect(plan.overwrites).toEqual(["docs/"]);
  });
  it("checks the path length with the current folder in front", () => {
    const deep = "d".repeat(1000);
    const picked = [{ file: fake("x", 1), path: "x".repeat(30) }];
    expect(planUpload(picked, [], "").problems).toEqual([]);
    expect(planUpload(picked, [], deep).problems[0]).toContain("1024");
  });
  it("refuses a file bigger than one request, bad paths and duplicates", () => {
    const plan = planUpload([{ file: fake("big.bin", BATCH_BYTES + 1), path: "big.bin" }, { file: fake("x", 1), path: "a//x" }, { file: fake("c", 1), path: "c" }, { file: fake("c", 1), path: "c" }], []);
    expect(plan.problems).toHaveLength(3);
    expect(plan.problems[0]).toContain("网页一次最多上传");
    expect(plan.problems[2]).toContain("两个同名文件");
  });
});

describe("batched upload", () => {
  it("splits into batches under the body limit, in order", () => {
    const f = (n: string, s: number) => ({ file: fake(n, s), path: n });
    expect(uploadBatches([f("a", 600 * MB), f("b", 600 * MB), f("c", 1)]).map((b) => b.map((x) => x.path))).toEqual([["a"], ["b", "c"]]);
  });
  it("sends the batches one after another into the folder, with progress over all bytes", async () => {
    const seen: string[] = [];
    const progress: number[] = [];
    const send = vi.fn(async (_pid: string, dir: string, files: { path: string }[], on?: (f: number) => void) => {
      on?.(0.5);
      seen.push(`${dir}:${files.map((x) => x.path).join(",")}`);
      return { files: files.map((x) => entry(joinPath(dir, x.path))) };
    });
    const done = await uploadProjectFiles("p1", "in", [{ file: fake("a", 600 * MB), path: "a" }, { file: fake("b", 600 * MB), path: "sub/b" }], (f) => progress.push(f), undefined, send);
    expect(seen).toEqual(["in:a", "in:sub/b"]);
    expect(done.map((e) => e.path)).toEqual(["in/a", "in/sub/b"]);
    expect(progress.at(-1)).toBe(1);
    expect(progress.every((p, i) => i === 0 || p >= progress[i - 1]!)).toBe(true);
  });
  it("names what was stored when a later batch fails, and explains a 413", async () => {
    let n = 0;
    const send = async (_p: string, _d: string, files: { path: string }[]) => {
      if (++n === 2) throw new ApiError(409, "共享文件夹还没有开通", "folder_unavailable");
      return { files: files.map((x) => entry(x.path)) };
    };
    const err = await uploadProjectFiles("p1", "", [{ file: fake("a", 600 * MB), path: "a" }, { file: fake("b", 600 * MB), path: "b" }], undefined, undefined, send).catch((e) => e);
    expect(err).toBeInstanceOf(UploadBatchError);
    expect(uploadErrorText(err)).toBe("已上传 1 个文件，其余 1 个没有上传：共享文件夹还没有开通");
    expect(uploadErrorText(new ApiError(413, "too big"))).toContain("请分几次上传");
  });
  it("stops before the next batch once cancelled", async () => {
    const ctl = new AbortController();
    const send = vi.fn(async (_p: string, _d: string, files: { path: string }[]) => (ctl.abort(), { files: files.map((x) => entry(x.path)) }));
    const err = await uploadProjectFiles("p1", "", [{ file: fake("a", 600 * MB), path: "a" }, { file: fake("b", 600 * MB), path: "b" }], undefined, ctl.signal, send).catch((e) => e);
    expect(send).toHaveBeenCalledTimes(1);
    expect(err.cause).toBeInstanceOf(UploadCancelled);
    expect(err.done.map((e: FileEntry) => e.path)).toEqual(["a"]);
  });
});

describe("folders", () => {
  const inFolder = (name: string, rel: string) => {
    const f = fake(name, 1);
    Object.defineProperty(f, "webkitRelativePath", { value: rel });
    return f;
  };
  it("skips system files inside a picked folder and counts them; other dot files are kept", () => {
    const got = collectFiles([inFolder("a.csv", "data/a.csv"), inFolder(".DS_Store", "data/.DS_Store"), inFolder("HEAD", "data/.git/HEAD"), inFolder(".env", "data/.env"), fake("x.md", 1)]);
    expect(got.files.map((f) => f.path)).toEqual(["data/a.csv", "data/.env", "x.md"]);
    expect(got.skipped).toBe(2);
  });
  it("walks a dropped folder with webkitGetAsEntry, keeping its structure", async () => {
    const fileEntry = (path: string) => ({ isFile: true, isDirectory: false, name: path.split("/").pop()!, fullPath: `/${path}`, file: (ok: (f: File) => void) => ok(fake(path.split("/").pop()!, 3)) });
    const dirEntry = (path: string, children: object[]) => {
      let read = false;
      return { isFile: false, isDirectory: true, name: path, fullPath: `/${path}`, createReader: () => ({ readEntries: (ok: (e: object[]) => void) => ok(read ? [] : ((read = true), children)) }) };
    };
    const tree = dirEntry("exports", [fileEntry("exports/a.csv"), dirEntry("exports/q4", [fileEntry("exports/q4/b.json")]), fileEntry("exports/.DS_Store")]);
    const items = [tree, fileEntry("notes.md")].map((e) => ({ kind: "file", webkitGetAsEntry: () => e }));
    const got = await droppedFiles({ items, files: [] } as unknown as DataTransfer);
    const picked = collectFiles(got.files);
    expect(picked.files.map((f) => f.path)).toEqual(["exports/a.csv", "exports/q4/b.json", "notes.md"]);
    expect(picked.skipped).toBe(1);
  });
  it("without webkitGetAsEntry, recognises a dropped folder instead of uploading it", async () => {
    const dir = new File([], "exports");
    Object.defineProperty(dir, "slice", { value: () => ({ arrayBuffer: () => Promise.reject(new Error("is a directory")) }) });
    const got = await droppedFiles({ items: [], files: [dir, new File(["x"], "a.txt", { type: "text/plain" }), new File([], "Makefile")] } as unknown as DataTransfer);
    expect(got.folders).toBe(1);
    expect(got.files.map((f) => f.file.name)).toEqual(["a.txt", "Makefile"]);
  });
});

describe("ProjectFiles page", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  async function mountPage(byDir: Record<string, Listing>, at = "/projects/p1/files") {
    const calls: { method: string; url: string; body?: string }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const req = input as Request;
      const url = new URL(req.url);
      calls.push({ method: req.method, url: req.url, ...(req.method === "POST" ? { body: await req.clone().text() } : {}) });
      if (req.method === "DELETE" || url.pathname.endsWith("/folders")) return new Response(null, { status: 204 });
      const body = url.pathname === "/api/projects/p1/files" ? byDir[url.searchParams.get("dir") ?? ""] ?? listing([]) : { id: "p1", name: "agent team" };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/projects/:pid/files", component: ProjectFiles }, { path: "/settings/storage", component: { template: "<p />" } }] });
    await router.push(at);
    const w = mount(ProjectFiles, {
      global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }) }], router] as never[] },
      attachTo: document.body,
    });
    await flushPromises();
    return { w, calls, router };
  }

  it("says why and links to the setting until the shared folder is on", async () => {
    const { w } = await mountPage({ "": listing([], { available: false, reason: "共享文件夹正在自动开通或暂时不可用，稍后再试（状态见 设置 → 共享文件夹）" }) });
    expect(w.get("[data-testid=files-unavailable]").text()).toContain("正在自动开通");
    expect(w.get("[data-testid=files-unavailable] a").attributes("href")).toBe("/settings/storage");
    expect(w.get("[data-testid=files-unavailable] a").text()).toBe("正在开通，去查看");
    expect(w.find("[data-testid=files-drop]").exists()).toBe(false);
  });

  it("lists folders and files, opens a folder and walks back by the breadcrumb", async () => {
    const { w, router } = await mountPage({
      "": listing([folder("data"), entry("notes.md", { size: 1536 })]),
      data: listing([entry("data/a.csv")], { dir: "data" }),
    });
    expect(w.get("[data-testid=files-notice]").text()).toContain("不要放密钥或密码");
    expect(w.get("[data-testid=files-notice]").text()).not.toContain("统筹");
    const rows = w.findAll("[role=row][data-path]");
    expect(rows.map((r) => r.attributes("data-path"))).toEqual(["data", "notes.md"]);
    expect(rows[1].text()).toContain("1.5 KB");
    expect(rows[1].get("a[download]").attributes("href")).toBe("/api/projects/p1/files/download?path=notes.md");
    await rows[0].get("button").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.dir).toBe("data");
    expect(w.findAll("[role=row][data-path]").map((r) => r.attributes("data-path"))).toEqual(["data/a.csv"]);
    expect(w.get("[data-testid=files-crumbs]").text()).toContain("data");
    await w.findAll("[data-testid=files-crumbs] button")[0].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.dir).toBeUndefined();
  });

  it("shows the empty state", async () => {
    const { w } = await mountPage({ "": listing([]) });
    expect(w.get("[data-testid=files-empty]").text()).toContain("还没有文件");
  });

  it("asks before replacing, then uploads into the current folder with dir and paths before the files", async () => {
    const { w } = await mountPage({ data: listing([entry("data/a.csv")], { dir: "data" }) }, "/projects/p1/files?dir=data");
    const forms: FormData[] = [];
    vi.spyOn(XMLHttpRequest.prototype, "open").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "setRequestHeader").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation((body) => void forms.push(body as FormData));
    const input = w.get("[data-testid=files-input]");
    Object.defineProperty(input.element, "files", { value: [fake("a.csv", 4), fake("b.csv", 5)], configurable: true });
    await input.trigger("change");
    await flushPromises();
    expect(forms).toHaveLength(0);
    const dialog = [...document.body.querySelectorAll("[role=alertdialog]")].find((d) => d.textContent?.includes("替换已有的文件"))!;
    expect(dialog.textContent).toContain("a.csv");
    [...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "替换并上传")!.click();
    await flushPromises();
    expect(forms).toHaveLength(1);
    const parts = [...forms[0].entries()].map(([k, v]) => (typeof v === "string" ? `${k}=${v}` : `${k}:${(v as File).name}`));
    expect(parts).toEqual(["dir=data", "path=a.csv", "path=b.csv", "file:a.csv", "file:b.csv"]);
  });

  it("says so when files are dropped while an upload is running", async () => {
    const { w } = await mountPage({ "": listing([]) });
    const warn = vi.spyOn(toast, "warning").mockImplementation(() => {});
    const forms: FormData[] = [];
    vi.spyOn(XMLHttpRequest.prototype, "open").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "setRequestHeader").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation((body) => void forms.push(body as FormData)); // never answers: stays busy
    const drop = w.get("[data-testid=files-drop]");
    await drop.trigger("drop", { dataTransfer: { items: [], files: [new File(["x"], "a.txt", { type: "text/plain" })] } });
    await flushPromises();
    expect(forms).toHaveLength(1);
    expect(w.find("[role=progressbar]").exists()).toBe(true);
    await drop.trigger("drop", { dataTransfer: { items: [], files: [fake("late.txt", 1)] } });
    await flushPromises();
    expect(warn).toHaveBeenLastCalledWith("正在上传，等这一批传完再拖入新的文件");
    expect(forms).toHaveLength(1);
  });

  it("cancels a running upload, and aborts it when the page goes away", async () => {
    const { w } = await mountPage({ "": listing([]) });
    const info = vi.spyOn(toast, "info").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "open").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation(() => {}); // never answers
    const abort = vi.spyOn(XMLHttpRequest.prototype, "abort").mockImplementation(function (this: XMLHttpRequest) {
      this.onabort?.(new ProgressEvent("abort"));
    });
    const drop = w.get("[data-testid=files-drop]");
    await drop.trigger("drop", { dataTransfer: { items: [], files: [new File(["x"], "a.txt", { type: "text/plain" })] } });
    await flushPromises();
    await w.get("[data-testid=files-cancel]").trigger("click");
    await flushPromises();
    expect(abort).toHaveBeenCalledTimes(1);
    expect(info).toHaveBeenCalledWith("已取消上传");
    expect(w.find("[role=progressbar]").exists()).toBe(false);
    expect(w.find("[data-testid=files-problems]").exists()).toBe(false);
    await drop.trigger("drop", { dataTransfer: { items: [], files: [new File(["x"], "b.txt", { type: "text/plain" })] } });
    await flushPromises();
    w.unmount();
    await flushPromises();
    expect(abort).toHaveBeenCalledTimes(2);
  });

  it("says so when an empty folder is dropped", async () => {
    const { w } = await mountPage({ "": listing([]) });
    const warn = vi.spyOn(toast, "warning").mockImplementation(() => {});
    const empty = { isFile: false, isDirectory: true, name: "empty", fullPath: "/empty", createReader: () => ({ readEntries: (ok: (e: object[]) => void) => ok([]) }) };
    await w.get("[data-testid=files-drop]").trigger("drop", { dataTransfer: { items: [{ kind: "file", webkitGetAsEntry: () => empty }], files: [] } });
    await flushPromises();
    expect(warn).toHaveBeenCalledWith("选中的文件夹是空的，没有可上传的文件");
  });

  it("refreshes the listing from the header and has no delete button on .deliverables", async () => {
    const { w, calls } = await mountPage({ "": listing([folder(".deliverables"), folder("data")]) });
    expect(w.find("button[aria-label='删除 .deliverables']").exists()).toBe(false);
    expect(w.find("button[aria-label='删除 data']").exists()).toBe(true);
    const lists = () => calls.filter((c) => c.url.includes("/api/projects/p1/files?")).length;
    const before = lists();
    await w.get("[data-testid=files-refresh]").trigger("click");
    await flushPromises();
    expect(lists()).toBe(before + 1);
  });

  it("creates a folder inside the current one", async () => {
    const { w, calls } = await mountPage({ data: listing([], { dir: "data" }) }, "/projects/p1/files?dir=data");
    await w.findAll("button").find((b) => b.text().includes("新建文件夹"))!.trigger("click");
    await flushPromises();
    const input = document.body.querySelector("input[aria-label=文件夹名称]") as HTMLInputElement;
    input.value = "raw";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "新建")!.click();
    await flushPromises();
    const post = calls.find((c) => c.url.endsWith("/files/folders"));
    expect(JSON.parse(post!.body!)).toEqual({ path: "data/raw" });
  });

  it("deletes a folder after confirming", async () => {
    const { w, calls } = await mountPage({ "": listing([folder("data")]) });
    await w.get("button[aria-label='删除 data']").trigger("click");
    await flushPromises();
    const dialog = [...document.body.querySelectorAll("[role=alertdialog]")].find((d) => d.textContent?.includes("删除这个文件夹？"))!;
    expect(dialog.textContent).toContain("里面的所有文件");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    [...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "删除")!.click();
    await flushPromises();
    expect(calls.find((c) => c.method === "DELETE")?.url).toContain("/api/projects/p1/files?path=data");
  });
});
