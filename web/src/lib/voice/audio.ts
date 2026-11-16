// Browser audio: the microphone stream, the device list and a level meter.

export interface MicDevice {
  deviceId: string;
  label: string;
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

/** Audio inputs; labels are empty until the page has microphone permission. */
export async function listMics(): Promise<{ devices: MicDevice[]; labelled: boolean }> {
  const md = globalThis.navigator?.mediaDevices;
  if (!md?.enumerateDevices) return { devices: [], labelled: false };
  const all = await md.enumerateDevices();
  const inputs = all.filter((d) => d.kind === "audioinput" && d.deviceId !== "default" && d.deviceId !== "communications");
  return {
    devices: inputs.map((d, i) => ({ deviceId: d.deviceId, label: d.label || `麦克风 ${i + 1}` })),
    labelled: inputs.some((d) => d.label),
  };
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
