import { describe, expect, it } from "vitest";
import { sharedFolderLine } from "@/pages/settings/nodes/nodes";

describe("sharedFolderLine", () => {
  it("is absent when the daemon cannot use the folder", () => {
    expect(sharedFolderLine({ online: true })).toBeNull();
  });
  it("shows the mount state, the Docker mode and the error", () => {
    expect(sharedFolderLine({ online: true, shared_folder: { mode: "daemon", state: "mounted", error: "" } })).toEqual({ text: "共享文件夹：已挂载", tone: "ok" });
    expect(sharedFolderLine({ online: true, shared_folder: { mode: "external", state: "error", error: "not a JuiceFS mount" } })).toEqual({
      text: "共享文件夹（Docker 卷）：挂载失败", tone: "error", error: "not a JuiceFS mount",
    });
    expect(sharedFolderLine({ online: false, shared_folder: { mode: "daemon", state: "mounted", error: "" } })?.tone).toBe("muted");
  });
});
