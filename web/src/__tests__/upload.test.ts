// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";
import { directUploader, STORAGE_FALLBACK_NOTE } from "@/components/app/attachments";
import { pickImages } from "@/lib/images";
import { uploadSharedFiles, UploadBatchError, uploadErrorText } from "@/lib/sharedFiles";
import { toast } from "@/lib/toast";
import { PartError, UploadCancelled, uploadFile, type PutPart, type UploadApi } from "@/lib/upload";

const MB = 1 << 20;

/** A File of the given size without allocating it (slice still works on the real 1-byte content). */
function fake(name: string, size: number, type = ""): File {
  const f = new File(["x"], name, { type });
  Object.defineProperty(f, "size", { value: size });
  return f;
}

/** A fake console for one multipart flow: records every call. */
function fakeApi(o: { partSize?: number; parts?: number; kind?: "attachment" | "shared"; create?: () => Promise<never> } = {}) {
  const calls: string[] = [];
  const partAsks: number[][] = [];
  let completed: { number: number; etag: string }[] = [];
  let urlGen = 0;
  const api: UploadApi = {
    create: vi.fn(async (body) => {
      calls.push(`create ${body.kind} ${body.name} ${body.size}`);
      if (o.create) return o.create();
      const partSize = o.partSize ?? 16 * MB;
      return { upload_id: "img_up1", id: "img_up1", kind: body.kind, part_size: partSize, parts: o.parts ?? Math.max(1, Math.ceil(body.size / partSize)), expires_at: "2026-11-25T15:00:00Z" };
    }),
    parts: vi.fn(async (_id, numbers) => {
      partAsks.push(numbers);
      urlGen++;
      return { urls: numbers.map((n: number) => ({ number: n, url: `https://s3.test/p${n}?gen=${urlGen}` })) };
    }),
    complete: vi.fn(async (_id, parts) => {
      completed = parts;
      calls.push("complete");
      return o.kind === "shared"
        ? { kind: "shared" as const, shared_file: { path: "data/a.bin", size: 1, content_type: "", sha256: "", overwritten: false, created_at: "", updated_at: "", uploaded_by: "user" } }
        : { kind: "attachment" as const, attachment: { id: "img_up1", name: "a", content_type: "application/zip", size: 1, sha256: "", created_at: "", url: "" } };
    }),
    abort: vi.fn(async () => void calls.push("abort")),
  };
  return { api, calls, partAsks, completed: () => completed };
}

afterEach(() => vi.restoreAllMocks());

describe("multipart direct upload", () => {
  it("PUTs every part (3 at a time), keeps the ETags in order and reports progress up to the full size", async () => {
    const { api, calls, completed } = fakeApi({ partSize: 16 * MB });
    let inFlight = 0;
    let maxInFlight = 0;
    const put: PutPart = async (url, body, onProgress) => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      onProgress(body.size / 2);
      await new Promise((r) => setTimeout(r, 5));
      onProgress(body.size);
      inFlight--;
      return `"etag-${new URL(url).pathname.slice(2)}"`;
    };
    const progress: number[] = [];
    const done = await uploadFile(fake("big.zip", 100 * MB), api, { kind: "attachment", put, onProgress: (s) => progress.push(s) });
    expect(done.attachment?.id).toBe("img_up1");
    expect(calls).toEqual([`create attachment big.zip ${100 * MB}`, "complete"]);
    expect(completed().map((p) => p.number)).toEqual([1, 2, 3, 4, 5, 6, 7]);
    expect(completed()[0]).toEqual({ number: 1, etag: '"etag-1"' });
    expect(maxInFlight).toBe(3);
    expect(progress.at(-1)).toBe(100 * MB);
    expect(progress.every((p) => p <= 100 * MB)).toBe(true);
  });

  it("retries a failed part (asking for a fresh URL after a 403) without redoing the others", async () => {
    const { api, partAsks } = fakeApi({ partSize: 16 * MB });
    const tries = new Map<number, number>();
    const put: PutPart = async (url) => {
      const n = Number(new URL(url).pathname.slice(2));
      tries.set(n, (tries.get(n) ?? 0) + 1);
      if (n === 2 && tries.get(n) === 1) throw new PartError(403, "expired");
      if (n === 2 && tries.get(n) === 2) throw new PartError(0, "network");
      return `e${n}`;
    };
    const done = await uploadFile(fake("a.bin", 40 * MB), api, { kind: "attachment", put, retryDelayMs: 0 });
    expect(done.kind).toBe("attachment");
    expect(Object.fromEntries(tries)).toEqual({ 1: 1, 2: 3, 3: 1 });
    expect(partAsks).toEqual([[1, 2, 3], [2]]);
  });

  it("gives up after the attempts, stops the other parts and deletes the upload", async () => {
    const { api, calls } = fakeApi({ partSize: 16 * MB });
    const put: PutPart = async (url, _b, _p, signal) => {
      if (url.includes("/p1")) throw new PartError(500, "分片上传失败（HTTP 500）");
      await new Promise((r, j) => {
        setTimeout(r, 50);
        signal.addEventListener("abort", () => j(new UploadCancelled()));
      });
      return "e";
    };
    const err = await uploadFile(fake("a.bin", 40 * MB), api, { kind: "attachment", put, retryDelayMs: 0 }).catch((e) => e);
    expect(err).toBeInstanceOf(PartError);
    expect((err as Error).message).toBe("分片上传失败（HTTP 500）");
    await vi.waitFor(() => expect(calls).toContain("abort"));
    expect(calls).not.toContain("complete");
  });

  it("cancels: running PUTs are aborted and the upload is deleted", async () => {
    const { api, calls } = fakeApi();
    const ctl = new AbortController();
    let started!: () => void;
    const running = new Promise<void>((r) => (started = r));
    const put: PutPart = (_u, _b, _p, signal) =>
      new Promise((_r, reject) => {
        started();
        signal.addEventListener("abort", () => reject(new UploadCancelled()));
      });
    const p = uploadFile(fake("a.bin", 10 * MB), api, { kind: "attachment", put, signal: ctl.signal });
    await running;
    ctl.abort();
    await expect(p).rejects.toBeInstanceOf(UploadCancelled);
    await vi.waitFor(() => expect(calls).toContain("abort"));
  });

  it("refuses files over 200 MB before uploading", () => {
    const { ok, problems } = pickImages([fake("huge.iso", 200 * MB + 1)], 0);
    expect(ok).toEqual([]);
    expect(problems).toEqual(["huge.iso 超过 200 MB"]);
  });
});

describe("message attachments without object storage", () => {
  const unavailable = () => Promise.reject(new ApiError(503, "没有配置对象存储", "storage_unavailable"));

  it("falls back to the base64 upload for files up to 5 MB, with a notice", async () => {
    const { api } = fakeApi({ create: unavailable });
    const warn = vi.spyOn(toast, "warning").mockImplementation(() => undefined);
    const posted: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const req = input as Request;
      posted.push(new URL(req.url, "http://x").pathname);
      return new Response(JSON.stringify({ id: "img_old" }), { status: 201, headers: { "Content-Type": "application/json" } });
    });
    const upload = directUploader(() => api);
    const progress: number[] = [];
    const small = new File(["png bytes"], "a.png", { type: "image/png" });
    if (!small.arrayBuffer) Object.defineProperty(small, "arrayBuffer", { value: async () => new TextEncoder().encode("png bytes").buffer });
    expect(await upload("p1", small, { onProgress: (f) => progress.push(f) })).toEqual({ id: "img_old" });
    expect(posted).toEqual(["/api/projects/p1/attachments"]);
    expect(warn).toHaveBeenCalledWith(STORAGE_FALLBACK_NOTE);
    expect(progress.at(-1)).toBe(1);
  });

  it("refuses a larger file with a clear reason", async () => {
    const { api } = fakeApi({ create: unavailable });
    const fetch = vi.spyOn(globalThis, "fetch");
    await expect(directUploader(() => api)("p1", fake("a.zip", 6 * MB))).rejects.toThrow("文件存储暂时不可用，现在只能上传 5 MB 以内的文件（这个文件 6 MB）");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("other refusals are not retried the old way", async () => {
    const { api } = fakeApi({ create: () => Promise.reject(new ApiError(413, "附件不能超过 200 MB", "file_too_large")) });
    const fetch = vi.spyOn(globalThis, "fetch");
    await expect(directUploader(() => api)("p1", fake("a.zip", MB))).rejects.toThrow("附件不能超过 200 MB");
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("shared files upload", () => {
  it("uploads each file straight to storage at its path, overwriting, with progress over all bytes", async () => {
    const { api } = fakeApi({ kind: "shared" });
    const creates: string[] = [];
    const put: PutPart = async (_u, body, onProgress) => (onProgress(body.size), "e");
    const progress: number[] = [];
    const res = await uploadSharedFiles("p1", [{ file: fake("a.bin", 3 * MB), path: "data/a.bin" }, { file: fake("b.bin", MB), path: "b.bin" }], (f) => progress.push(f), {
      api: { ...api, create: (b) => (creates.push(`${b.kind} ${b.path} overwrite=${b.overwrite}`), api.create(b)) },
      put,
    });
    expect(creates).toEqual(["shared data/a.bin overwrite=true", "shared b.bin overwrite=true"]);
    expect(res.files).toHaveLength(2);
    expect(progress.every((p, i) => i === 0 || p >= progress[i - 1]!)).toBe(true);
    expect(progress.at(-1)).toBe(1);
  });

  it("names what was stored when a later file is refused (quota)", async () => {
    const { api } = fakeApi({ kind: "shared" });
    let n = 0;
    const quota = new ApiError(409, "项目共享文件合计不能超过 5 GB（已用 4.9 GB）", "quota_exceeded");
    const err = await uploadSharedFiles("p1", [{ file: fake("a.bin", MB), path: "a.bin" }, { file: fake("b.bin", MB), path: "b.bin" }], undefined, {
      api: { ...api, create: (b) => (++n === 2 ? Promise.reject(quota) : api.create(b)) },
      put: async () => "e",
    }).catch((e) => e);
    expect(err).toBeInstanceOf(UploadBatchError);
    expect(uploadErrorText(err)).toBe("已上传 1 个文件，其余 1 个没有上传：项目共享文件合计不能超过 5 GB（已用 4.9 GB）");
  });

  it("shows the console's quota message as it is", () => {
    expect(uploadErrorText(new ApiError(409, "项目共享文件已用 4.9 GB / 5 GB，这个文件 300 MB 放不下", "quota_exceeded"))).toBe("项目共享文件已用 4.9 GB / 5 GB，这个文件 300 MB 放不下");
  });

  it("falls back to the old form upload without object storage, refusing files over 20 MB", async () => {
    const { api } = fakeApi({ create: () => Promise.reject(new ApiError(503, "no storage", "storage_unavailable")) });
    const legacy = vi.fn(async () => ({ files: [{ path: "a.txt", size: 1, content_type: "", sha256: "", overwritten: false, created_at: "", updated_at: "", uploaded_by: "user" }], total_bytes: 1 }));
    const res = await uploadSharedFiles("p1", [{ file: fake("a.txt", 1), path: "a.txt" }], undefined, { api, legacy });
    expect(res.files?.map((f) => f.path)).toEqual(["a.txt"]);
    expect(legacy).toHaveBeenCalledOnce();
    const err = await uploadSharedFiles("p1", [{ file: fake("big.bin", 21 * MB), path: "big.bin" }], undefined, { api, legacy }).catch((e) => e);
    expect(err).toBeInstanceOf(UploadBatchError);
    expect(uploadErrorText(err)).toContain("文件存储暂时不可用，现在每个文件最多 20 MB（big.bin 超了）");
  });
});
