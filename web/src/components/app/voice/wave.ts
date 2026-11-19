// Mapping a meter reading (startMeter: RMS × 5, 0..1) to a waveform bar height.

/** Below this the microphone is treated as silent: background noise stays a flat row of dots. */
export const WAVE_GATE = 0.03;
/** The silent bar: a dot as tall as it is wide-ish. */
export const WAVE_MIN = 3;

/** Bar height in px for a level: WAVE_MIN when silent, up to max for loud speech. */
export function barHeight(level: number, max: number): number {
  const v = Math.min(1, Math.max(0, (level - WAVE_GATE) / (0.7 - WAVE_GATE)));
  return Math.round(WAVE_MIN + (max - WAVE_MIN) * Math.pow(v, 0.6));
}

/** The waveform samples the level once per meter tick (startMeter reports every 50 ms). */
export const WAVE_TICK_MS = 50;
