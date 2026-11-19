// @vitest-environment jsdom
// Shared files (api.md §3.5.1): sizes, path rules mirrored from the server, the upload plan
// (overwrites, limits), uploader labels, the shared_file event, and the page: notice, list,
// usage, empty state, overwrite confirmation, delete confirmation.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { applyProjectEvent, PROJECT_EVENTS } from "@/api/events";
import { keys } from "@/api/queries";
import type { SharedFile, SharedFileList } from "@/api/views";
import {
  SHARED_LIMITS,
  downloadUrl,
  filterShared,
  formatSize,
  joinSharedPath,
  pickedFiles,
  planUpload,
  sharedPathProblem,
  uploadBatches,
  UploadBatchError,
  uploadErrorText,
  uploaderLabel,
  uploadSharedFiles,
  SHARED_BATCH_BYTES,
  collectFiles,
  droppedFiles,
} from "@/lib/sharedFiles";
import { ApiError } from "@/api/client";
import SharedFiles from "@/pages/SharedFiles.vue";

const MB = 1 << 20;
const file = (path: string, size: number, extra: Partial<SharedFile> = {}): SharedFile => ({
  path,
  size,
  content_type: "text/plain",
  sha256: "0".repeat(64),
  uploaded_by: "user",
  created_at: "2026-11-19T10:00:00Z",
  updated_at: "2026-11-19T10:00:00Z",
  ...extra,
});
const listOf = (files: SharedFile[], extra: Partial<SharedFileList> = {}): SharedFileList => ({
  files,
  total_bytes: files.reduce((n, f) => n + f.size, 0),
  file_count: files.length,
  limits: SHARED_LIMITS,
  ...extra,
});
/** A File of the given size without allocating it. */
const fake = (name: string, size: number) => {
  const f = new File(["x"], name);
  Object.defineProperty(f, "size", { value: size });
  return f;
};

describe("formatSize", () => {
  it("prints binary units like the limits", () => {
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(820)).toBe("820 B");
    expect(formatSize(1536)).toBe("1.5 KB");
    expect(formatSize(20 * MB)).toBe("20 MB");
    expect(formatSize(500 * MB)).toBe("500 MB");
    expect(formatSize(1.25 * MB)).toBe("1.3 MB");
    expect(formatSize(undefined)).toBe("—");
  });
});

describe("sharedPathProblem", () => {
  it("accepts clean relative paths, sub-folders and non-ASCII names", () => {
    for (const p of ["notes.md", "data/sample.csv", "报告/2026-11.xlsx", "a b/c d.txt", "a/b/c/d/e/f/g/h", "x".repeat(255), "中".repeat(85)]) {
      expect(sharedPathProblem(p), p).toBeNull();
    }
  });
  it("refuses what the server refuses, with a reason", () => {
    const cases: [string, string][] = [
      ["", "空"],
      ["/etc/passwd", "绝对路径"],
      ["../secret", ".."],
      ["a/../../b", ".."],
      ["a//b", "空的目录名"],
      ["a/", "空的目录名"],
      [".env", "以 . 开头"],
      ["a/.git/config", "以 . 开头"],
      ["a\\b", "反斜杠"],
      ["a\nb", "控制字符"],
      ["a\u0085b", "控制字符"],
      ["a b", "控制字符"],
      [" a.txt", "空格"],
      ["data /a.txt", "空格"],
      ["a/b/c/d/e/f/g/h/i", "8 层"],
      ["x".repeat(256), "255 字节"],
      ["中".repeat(86), "255 字节"],
    ];
    for (const [p, why] of cases) expect(sharedPathProblem(p), JSON.stringify(p)).toContain(why);
  });
  it("joins the folder prefix", () => {
    expect(joinSharedPath("data/", "a.csv")).toBe("data/a.csv");
    expect(joinSharedPath("data", "a.csv")).toBe("data/a.csv");
    expect(joinSharedPath("", "a.csv")).toBe("a.csv");
    expect(pickedFiles([fake("a.csv", 1)], " data/ ")[0].path).toBe("data/a.csv");
    const inFolder = fake("b.txt", 1);
    Object.defineProperty(inFolder, "webkitRelativePath", { value: "exports/2026/b.txt" });
    expect(pickedFiles([inFolder], "")[0].path).toBe("exports/2026/b.txt");
  });
});

describe("planUpload", () => {
  it("lists the files it would overwrite and counts the project total by the difference", () => {
    const list = listOf([file("data/a.csv", 10 * MB), file("b.txt", 1)]);
    const plan = planUpload(pickedFiles([fake("a.csv", 2 * MB), fake("c.txt", 5)], "data"), list);
    expect(plan.problems).toEqual([]);
    expect(plan.overwrites).toEqual(["data/a.csv"]);
    expect(plan.totalAfter).toBe(2 * MB + 1 + 5);
  });
  it("refuses a file over 20 MB, bad paths and duplicates", () => {
    const plan = planUpload(
      [
        { file: fake("big.bin", 20 * MB + 1), path: "big.bin" },
        { file: fake("x", 1), path: "../x" },
        { file: fake("y", 1), path: "d/y" },
        { file: fake("y", 1), path: "d/y" },
      ],
      listOf([]),
    );
    expect(plan.problems.join("\n")).toContain("超过单个文件 20 MB 的上限");
    expect(plan.problems.join("\n")).toContain("../x：不能有 . 或 .. 目录");
    expect(plan.problems.join("\n")).toContain("d/y：这次上传里有两个同名文件");
  });
  it("refuses an upload past the project's 500 MB or 1000 files, but allows an overwrite that fits", () => {
    const full = listOf([file("old.bin", 10 * MB)], { total_bytes: 495 * MB });
    expect(planUpload([{ file: fake("n.bin", 6 * MB), path: "n.bin" }], full).problems[0]).toContain("超过 500 MB 的上限");
    expect(planUpload([{ file: fake("old.bin", 15 * MB), path: "old.bin" }], full).problems).toEqual([]);
    const many = listOf([], { file_count: 1000 });
    expect(planUpload([{ file: fake("n.txt", 1), path: "n.txt" }], many).problems[0]).toContain("1000 个的上限");
  });
});

describe("batched upload", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  /** A fake XMLHttpRequest: records each request's paths, reports progress, answers per request. */
  function fakeXhr(answer: (n: number, paths: string[]) => { status: number; body: unknown }) {
    const sent: string[][] = [];
    class FakeXhr {
      upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null };
      onload: (() => void) | null = null;
      onerror: (() => void) | null = null;
      status = 0;
      statusText = "";
      responseText = "";
      withCredentials = false;
      open() {}
      setRequestHeader() {}
      send(form: FormData) {
        const paths = form.getAll("path").map(String);
        sent.push(paths);
        const r = answer(sent.length, paths);
        queueMicrotask(() => {
          this.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 2 });
          this.status = r.status;
          this.responseText = JSON.stringify(r.body);
          this.onload?.();
        });
      }
    }
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
    return sent;
  }
  const up = (path: string) => ({ ...file(path, 1), overwritten: false });

  it("splits into batches of at most 80 MB, in order", () => {
    const files = [18, 18, 18, 18, 18, 18].map((m, i) => ({ file: fake(`f${i}`, m * MB), path: `f${i}` }));
    expect(uploadBatches(files).map((b) => b.map((f) => f.path))).toEqual([["f0", "f1", "f2", "f3"], ["f4", "f5"]]);
    expect(uploadBatches([{ file: fake("a", 1), path: "a" }]).length).toBe(1);
    expect(SHARED_BATCH_BYTES).toBeLessThanOrEqual(5 * 20 * MB);
  });

  it("sends a 108 MB folder as two requests under the server's body limit and reports progress over all bytes", async () => {
    const sent = fakeXhr((_n, paths) => ({ status: 201, body: { files: paths.map(up), total_bytes: 7 } }));
    const files = [18, 18, 18, 18, 18, 18].map((m, i) => ({ file: fake(`f${i}`, m * MB), path: `dir/f${i}` }));
    const progress: number[] = [];
    const res = await uploadSharedFiles("p1", files, (f) => progress.push(f));
    expect(sent).toEqual([["dir/f0", "dir/f1", "dir/f2", "dir/f3"], ["dir/f4", "dir/f5"]]);
    expect(res.files?.map((f) => f.path)).toEqual(files.map((f) => f.path));
    expect(progress.every((p, i) => i === 0 || p >= progress[i - 1])).toBe(true);
    expect(progress.at(-1)).toBe(1);
  });

  it("names what was stored when a later batch fails", async () => {
    fakeXhr((n, paths) =>
      n === 1 ? { status: 201, body: { files: paths.map(up), total_bytes: 1 } } : { status: 409, body: { detail: "项目共享文件合计不能超过 500 MB", code: "quota_exceeded" } },
    );
    const files = [18, 18, 18, 18, 18].map((m, i) => ({ file: fake(`f${i}`, m * MB), path: `f${i}` }));
    const err = await uploadSharedFiles("p1", files).catch((e) => e);
    expect(err).toBeInstanceOf(UploadBatchError);
    expect(uploadErrorText(err)).toBe("已上传 4 个文件，其余 1 个没有上传：项目共享文件合计不能超过 500 MB");
  });

  it("explains a 413 by what it is", () => {
    expect(uploadErrorText(new ApiError(413, "单个文件不能超过 20 MB（这个文件 21.0 MB）", "file_too_large"))).toBe("单个文件不能超过 20 MB（这个文件 21.0 MB）");
    expect(uploadErrorText(new ApiError(413, "请求体太大", "too_large"))).toContain("这次上传的请求太大");
    expect(uploadErrorText(new ApiError(413, "请求体太大", "too_large"))).not.toContain("单个文件");
  });
});

describe("folders", () => {
  const inFolder = (name: string, rel: string) => {
    const f = fake(name, 1);
    Object.defineProperty(f, "webkitRelativePath", { value: rel });
    return f;
  };
  it("skips hidden files and folders inside a picked folder and counts them; a hidden file picked alone is kept", () => {
    const got = collectFiles(
      [inFolder("a.csv", "data/a.csv"), inFolder(".DS_Store", "data/.DS_Store"), inFolder("HEAD", "data/.git/HEAD"), fake(".env", 1)],
      "up/",
    );
    expect(got.files.map((f) => f.path)).toEqual(["up/data/a.csv", "up/.env"]);
    expect(got.skipped).toBe(2);
  });
  it("walks a dropped folder with webkitGetAsEntry, keeping its structure", async () => {
    const fileEntry = (path: string) => ({ isFile: true, isDirectory: false, name: path.split("/").pop()!, fullPath: `/${path}`, file: (ok: (f: File) => void) => ok(fake(path.split("/").pop()!, 3)) });
    const dirEntry = (path: string, children: object[]) => {
      let read = false;
      return {
        isFile: false,
        isDirectory: true,
        name: path,
        fullPath: `/${path}`,
        createReader: () => ({ readEntries: (ok: (e: object[]) => void) => ok(read ? [] : ((read = true), children)) }),
      };
    };
    const tree = dirEntry("exports", [fileEntry("exports/a.csv"), dirEntry("exports/q4", [fileEntry("exports/q4/b.json")]), fileEntry("exports/.DS_Store")]);
    const loose = fileEntry("notes.md");
    const items = [tree, loose].map((e) => ({ kind: "file", webkitGetAsEntry: () => e }));
    const got = await droppedFiles({ items, files: [] } as unknown as DataTransfer);
    expect(got.folders).toBe(0);
    expect(got.files.map((f) => f.rel || f.file.name)).toEqual(["exports/a.csv", "exports/q4/b.json", "exports/.DS_Store", "notes.md"]);
    const picked = collectFiles(got.files, "");
    expect(picked.files.map((f) => f.path)).toEqual(["exports/a.csv", "exports/q4/b.json", "notes.md"]);
    expect(picked.skipped).toBe(1);
  });
  it("without webkitGetAsEntry, recognises a dropped folder instead of uploading it", async () => {
    const folder = new File([], "exports");
    Object.defineProperty(folder, "slice", { value: () => ({ arrayBuffer: () => Promise.reject(new Error("is a directory")) }) });
    const plain = new File(["x"], "a.txt", { type: "text/plain" });
    const got = await droppedFiles({ items: [], files: [folder, plain] } as unknown as DataTransfer);
    expect(got.folders).toBe(1);
    expect(got.files.map((f) => f.file.name)).toEqual(["a.txt"]);
  });
});

describe("labels and lists", () => {
  it("shows the uploader as 你 or name #thread", () => {
    expect(uploaderLabel({ uploaded_by: "user" })).toBe("你");
    expect(uploaderLabel({ uploaded_by: "dev-1", uploaded_by_thread_no: 31 })).toBe("dev-1 #31");
    expect(uploaderLabel({ uploaded_by: "coordinator" })).toBe("coordinator");
    expect(uploaderLabel({ uploaded_by: "" })).toBe("—");
  });
  it("filters by path text and sorts by path", () => {
    const all = [file("z.txt", 1), file("data/b.csv", 1), file("Data/A.csv", 1), file("notes.md", 1)];
    expect(filterShared(all, "").map((f) => f.path)).toEqual(["Data/A.csv", "data/b.csv", "notes.md", "z.txt"]);
    expect(filterShared(all, "data/").map((f) => f.path)).toEqual(["Data/A.csv", "data/b.csv"]);
  });
  it("builds an encoded download link", () => {
    expect(downloadUrl("p1", "报告/a b.csv")).toBe("/api/projects/p1/shared-files/download?path=%E6%8A%A5%E5%91%8A%2Fa%20b.csv");
  });
  it("refetches the list on shared_file events", () => {
    expect(PROJECT_EVENTS).toContain("shared_file");
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    applyProjectEvent(qc, "p1", "shared_file", { op: "write", path: "a.csv", by: "dev-1 #31" });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.sharedFiles("p1") });
  });
});

describe("SharedFiles page", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  async function mountPage(list: SharedFileList) {
    const calls: { method: string; url: string }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const req = input as Request;
      calls.push({ method: req.method, url: req.url });
      const body = req.url.includes("/shared-files") && req.method === "GET" ? list : req.method === "DELETE" ? null : { id: "p1", name: "agent team" };
      return body === null ? new Response(null, { status: 204 }) : new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/projects/:pid/shared-files", component: SharedFiles }] });
    await router.push("/projects/p1/shared-files");
    const w = mount(SharedFiles, {
      global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }) }], router] as never[] },
      attachTo: document.body,
    });
    await flushPromises();
    return { w, calls };
  }

  it("warns about secrets, lists files with size, uploader and usage", async () => {
    const { w } = await mountPage(listOf([file("data/a.csv", 1536, { uploaded_by: "dev-1", uploaded_by_thread_no: 31 }), file("notes.md", 20)]));
    expect(w.get("[data-testid=shared-notice]").text()).toContain("不要放密钥或密码");
    const rows = w.findAll("[role=row][data-path]");
    expect(rows.map((r) => r.attributes("data-path"))).toEqual(["data/a.csv", "notes.md"]);
    expect(rows[0].text()).toContain("1.5 KB");
    expect(rows[0].text()).toContain("dev-1 #31");
    expect(rows[1].text()).toContain("你");
    expect(rows[0].get("a[download]").attributes("href")).toBe("/api/projects/p1/shared-files/download?path=data%2Fa.csv");
    expect(w.get("[data-testid=shared-usage]").text()).toContain("已用 1.5 KB / 500 MB");
    await w.get("input[aria-label=搜索共享文件]").setValue("notes");
    expect(w.findAll("[role=row][data-path]").map((r) => r.attributes("data-path"))).toEqual(["notes.md"]);
  });

  it("shows the empty state", async () => {
    const { w } = await mountPage(listOf([]));
    expect(w.get("[data-testid=shared-empty]").text()).toContain("还没有共享文件");
  });

  it("asks before overwriting and refuses files over the limit without uploading", async () => {
    const { w } = await mountPage(listOf([file("data/a.csv", 10)]));
    const send = vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation(() => {});
    await w.get("input[aria-label=目录前缀]").setValue("data/");
    const input = w.get("[data-testid=shared-file-input]");
    Object.defineProperty(input.element, "files", { value: [fake("a.csv", 4)], configurable: true });
    await input.trigger("change");
    await flushPromises();
    const dialog = document.body.querySelector("[role=alertdialog]");
    expect(dialog?.textContent).toContain("覆盖已有的共享文件？");
    expect(dialog?.textContent).toContain("data/a.csv");
    expect(send).not.toHaveBeenCalled();

    Object.defineProperty(input.element, "files", { value: [fake("big.bin", 21 * MB)], configurable: true });
    await input.trigger("change");
    await flushPromises();
    expect(w.get("[data-testid=shared-problems]").text()).toContain("超过单个文件 20 MB 的上限");
    expect(send).not.toHaveBeenCalled();
  });

  it("uploads after the overwrite is confirmed, with path fields before the files, in order", async () => {
    const { w } = await mountPage(listOf([file("data/a.csv", 10)]));
    const forms: FormData[] = [];
    vi.spyOn(XMLHttpRequest.prototype, "open").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "setRequestHeader").mockImplementation(() => {});
    vi.spyOn(XMLHttpRequest.prototype, "send").mockImplementation((body) => void forms.push(body as FormData));
    await w.get("input[aria-label=目录前缀]").setValue("data/");
    const input = w.get("[data-testid=shared-file-input]");
    Object.defineProperty(input.element, "files", { value: [fake("a.csv", 4), fake("b.csv", 5)], configurable: true });
    await input.trigger("change");
    await flushPromises();
    expect(forms).toHaveLength(0);
    const dialog = [...document.body.querySelectorAll("[role=alertdialog]")].find((d) => d.textContent?.includes("覆盖已有的共享文件"))!;
    [...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "覆盖并上传")!.click();
    await flushPromises();
    expect(forms).toHaveLength(1);
    const entries = [...forms[0].entries()].map(([k, v]) => (typeof v === "string" ? `${k}=${v}` : `${k}:${(v as File).name}`));
    expect(entries).toEqual(["path=data/a.csv", "path=data/b.csv", "file:a.csv", "file:b.csv"]);
  });

  it("deletes after confirming", async () => {
    const { w, calls } = await mountPage(listOf([file("notes.md", 20)]));
    await w.get("button[aria-label='删除 notes.md']").trigger("click");
    await flushPromises();
    const dialog = [...document.body.querySelectorAll("[role=alertdialog]")].find((d) => d.textContent?.includes("删除这个共享文件？"));
    expect(dialog).toBeTruthy();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    const confirm = [...dialog!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "删除")!;
    confirm.click();
    await flushPromises();
    const del = calls.find((c) => c.method === "DELETE");
    expect(del?.url).toContain("/api/projects/p1/shared-files?path=notes.md");
  });
});
