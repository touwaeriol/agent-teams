// Images attached to a message: which files are accepted and why one is refused.
// The console checks the same rules again (core.NewAttachment): PNG, JPEG, GIF or WebP,
// at most 5 MB each (Claude's own per-image limit), at most 8 per message.

export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
export const IMAGE_ACCEPT = IMAGE_TYPES.join(",");
export const IMAGE_MAX_BYTES = 5 << 20;
export const IMAGE_MAX_COUNT = 8;

export function imageProblem(file: { type: string; size: number; name: string }): string | null {
  if (!IMAGE_TYPES.includes(file.type)) return `${file.name || "这个文件"} 不是 PNG、JPG、GIF 或 WebP 图片`;
  if (file.size > IMAGE_MAX_BYTES) return `${file.name || "图片"} 超过 5 MB`;
  return null;
}

/** Splits picked files into the ones to upload (within the per-message cap) and the reasons for the rest. */
export function pickImages<T extends { type: string; size: number; name: string }>(files: T[], already: number): { ok: T[]; problems: string[] } {
  const ok: T[] = [];
  const problems: string[] = [];
  for (const f of files) {
    const problem = imageProblem(f);
    if (problem) problems.push(problem);
    else if (already + ok.length >= IMAGE_MAX_COUNT) problems.push(`一条消息最多 ${IMAGE_MAX_COUNT} 张图片`);
    else ok.push(f);
  }
  return { ok, problems: [...new Set(problems)] };
}

/** Image files among pasted or dropped items. */
export function imageFiles(list: FileList | File[] | null | undefined): File[] {
  return Array.from(list ?? []).filter((f) => f.type.startsWith("image/"));
}

export const attachmentUrl = (id: string) => `/api/attachments/${encodeURIComponent(id)}`;
