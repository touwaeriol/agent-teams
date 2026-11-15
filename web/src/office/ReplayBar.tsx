// Replay controls: play/pause, 1× / 4× / 16× and a time scrubber.
import type { CSSProperties } from "react";
import { REPLAY_SPEEDS, type ReplayState } from "./replay";

function clock(ms: number): string {
  const d = new Date(ms);
  const two = (n: number) => String(n).padStart(2, "0");
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

const btn = (on: boolean): CSSProperties => ({
  height: 26, padding: "0 9px", borderRadius: 7, border: on ? "none" : "1px solid #4A4D59", cursor: "pointer",
  background: on ? "#F0EDE6" : "transparent", color: on ? "#1F1E1C" : "#E6E3DC", font: "inherit", fontSize: 12, fontWeight: on ? 600 : 400,
});

export function ReplayBar({ replay, style }: { replay: ReplayState; style?: CSSProperties }) {
  const { time, start, end, playing, speed } = replay;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, fontSize: 12, color: "#D8D4CB", ...style }}>
      <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
        <button type="button" style={{ ...btn(true), background: "#C8622A", color: "#FFFFFF" }} onClick={playing ? replay.pause : replay.play} aria-label={playing ? "暂停" : "播放"}>
          {playing ? "❚❚ 暂停" : "▶ 播放"}
        </button>
        {REPLAY_SPEEDS.map((s) => (
          <button key={s} type="button" style={btn(speed === s)} aria-pressed={speed === s} onClick={() => replay.setSpeed(s)}>
            {s}×
          </button>
        ))}
        <span style={{ marginLeft: "auto", fontFamily: "'JetBrains Mono', monospace", color: "#A8A49B" }}>{clock(time)}</span>
      </div>
      <input
        type="range"
        aria-label="回放进度"
        min={start}
        max={end}
        step={100}
        value={time}
        onChange={(e) => replay.seek(Number(e.target.value))}
        style={{ width: "100%", accentColor: "#C8622A" }}
      />
      <div style={{ display: "flex", justifyContent: "space-between", fontFamily: "'JetBrains Mono', monospace", color: "#7E8190", fontSize: 11 }}>
        <span>{clock(start)}</span>
        <span>{clock(end)}</span>
      </div>
    </div>
  );
}
