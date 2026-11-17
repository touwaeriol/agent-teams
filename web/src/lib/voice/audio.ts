// Browser audio: the microphone stream, the device list and a level meter.

export interface MicDevice {
  deviceId: string;
  label: string;
  /** Shown before permission; selecting it asks for permission instead. */
  placeholder?: boolean;
}

/** Why the microphone could not be opened, in words for the user. */
export function micErrorText(e: unknown): string {
  const name = (e as { name?: string })?.name ?? "";
  if (name === "NotAllowedError" || name === "SecurityError") return "浏览器没有允许使用麦克风，请在地址栏的权限设置里允许";
  if (name === "NotFoundError" || name === "OverconstrainedError") return "没有找到麦克风";
  if (name === "NotReadableError") return "麦克风被别的程序占用了";
  if (!globalThis.navigator?.mediaDevices?.getUserMedia) return "这个浏览器不支持录音（需要 HTTPS）";
  return "打不开麦克风";
}

export async function openMic(deviceId: string): Promise<MediaStream> {
  const md = navigator.mediaDevices;
  if (!md?.getUserMedia) throw Object.assign(new Error("no mediaDevices"), { name: "Unsupported" });
  const audio: MediaTrackConstraints = { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 };
  if (deviceId) audio.deviceId = { exact: deviceId };
  try {
    return await md.getUserMedia({ audio });
  } catch (e) {
    // The saved microphone is gone: fall back to the default one.
    if (deviceId && (e as { name?: string })?.name === "OverconstrainedError") return openMic("");
    throw e;
  }
}

export function stopStream(stream: MediaStream | null | undefined): void {
  stream?.getTracks().forEach((t) => t.stop());
}

export interface MicList {
  /** First the system default (deviceId ""), then one row per real microphone. */
  devices: MicDevice[];
  /** False before microphone permission: names (and ids) are hidden, rows are placeholders. */
  labelled: boolean;
}

type DeviceLike = Pick<MediaDeviceInfo, "deviceId" | "kind" | "label" | "groupId">;

const DEFAULT_PREFIX = /^(default|默认)\s*[-–—:：]\s*/i;

/**
 * Chrome lists the pseudo devices "default" ("Default - <name>") and "communications" next to the
 * real ones, and sometimes the same microphone more than once. One 系统默认 row stands for
 * "default" (with the real name when known), "communications" is dropped, and real microphones
 * are deduplicated by group and name. Before permission every id and label is empty: rows are
 * placeholders ("麦克风 N", id "placeholder:N") that can never match a saved choice.
 */
export function normalizeMics(all: DeviceLike[]): MicList {
  const inputs = all.filter((d) => d.kind === "audioinput");
  const labelled = inputs.some((d) => d.label);
  const def = inputs.find((d) => d.deviceId === "default");
  const defName = def?.label.replace(DEFAULT_PREFIX, "").trim() ?? "";
  const devices: MicDevice[] = [{ deviceId: "", label: defName ? `系统默认（${defName}）` : "系统默认" }];
  const seen = new Set<string>();
  for (const d of inputs) {
    if (d.deviceId === "default" || d.deviceId === "communications") continue;
    const key = d.label ? `${d.groupId}|${d.label}` : d.deviceId;
    if (key && seen.has(key)) continue;
    seen.add(key);
    const n = devices.length;
    devices.push(d.deviceId && d.label ? { deviceId: d.deviceId, label: d.label } : { deviceId: `placeholder:${n}`, label: d.label || `麦克风 ${n}`, placeholder: true });
  }
  return { devices, labelled };
}

/** The one row to check: the saved microphone if it is still there, otherwise 系统默认. */
export function selectedMic(list: MicDevice[], deviceId: string): string {
  return deviceId && list.some((d) => d.deviceId === deviceId && !d.placeholder) ? deviceId : "";
}

/** Audio inputs as rows for the mic menu (see normalizeMics). */
export async function listMics(): Promise<MicList> {
  const md = globalThis.navigator?.mediaDevices;
  if (!md?.enumerateDevices) return { devices: [{ deviceId: "", label: "系统默认" }], labelled: false };
  return normalizeMics(await md.enumerateDevices());
}

/** Asks for permission once (closing the stream right away) so device names show. */
export async function askMicPermission(): Promise<void> {
  stopStream(await openMic(""));
}

/** Calls onLevel(0..1) about 20 times a second until the returned function is called. */
export function startMeter(stream: MediaStream, onLevel: (level: number) => void): () => void {
  const Ctx = globalThis.AudioContext ?? (globalThis as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
  if (!Ctx) return () => {};
  const ctx = new Ctx();
  const source = ctx.createMediaStreamSource(stream);
  const analyser = ctx.createAnalyser();
  analyser.fftSize = 1024;
  source.connect(analyser);
  const buf = new Float32Array(analyser.fftSize);
  const timer = setInterval(() => {
    analyser.getFloatTimeDomainData(buf);
    let sum = 0;
    for (const v of buf) sum += v * v;
    onLevel(Math.min(1, Math.sqrt(sum / buf.length) * 5));
  }, 50);
  return () => {
    clearInterval(timer);
    source.disconnect();
    void ctx.close();
  };
}
