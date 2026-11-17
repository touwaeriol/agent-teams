# Voice input (语音输入)

The composers (coordinator chat and thread lead) get a mic button when an administrator has
configured a speech-to-text provider in 管理 → 语音输入. Every signed-in user can use it.

Two transports (管理 → 语音输入 → 连接方式):

- **经服务器中转** (`relay`, default). The browser streams microphone audio to the control plane
  over a WebSocket (`GET /api/voice/stream`) and the control plane relays it to OpenAI with the
  sealed key. The browser only ever talks to this site.
- **浏览器直连 OpenAI** (`webrtc`, optional; lower latency). The control plane mints a short-lived
  client secret and the browser connects to OpenAI itself over WebRTC. Needs the browser to reach
  `api.openai.com` (and UDP for WebRTC).

Why relay is the default: in production the key worked (测试 succeeded, `/api/voice/session` 200)
and the whole WebRTC flow worked from our server's network, but a user's browser spun forever —
their network path to OpenAI (proxy, region, blocked UDP) didn't work, and the SDP `fetch` had no
timeout. Relaying removes the browser → OpenAI leg, and every connect step now has a 10 s limit.

## Provider: OpenAI Realtime transcription

Checked against developers.openai.com (realtime transcription and WebSocket guides) on
2026-11-16/17:

- Model: `gpt-realtime-whisper` (streaming speech-to-text for realtime transcription sessions;
  $0.017/min). The admin can set another model id (`gpt-live-transcribe`, `gpt-4o-transcribe`, …).
- Session object (the same for both transports): `{"type": "transcription", "audio": {"input": {
  "format": {"type": "audio/pcm", "rate": 24000}, "noise_reduction": {"type": "near_field"} | null,
  "transcription": {"model", "language"?, "delay"?}, "turn_detection": null}}}`.
  - `gpt-realtime-whisper`: turn detection must be `null` (no VAD), `prompt` is not supported,
    `delay` (`minimal|low|medium|high|xhigh`) is supported only by this model.
  - `gpt-live-transcribe` / `gpt-transcribe` take `languages: [code]` instead of `language` (don't
    send both); older transcribe models get `server_vad`.
- Relay (server side): `wss://api.openai.com/v1/realtime?intent=transcription` (no `model` in
  the URL: "the model-based URL creates a conversation session that doesn't accept the
  transcription update"), header `Authorization: Bearer <API key>` plus
  `OpenAI-Safety-Identifier: <hash of the user id>`; send `session.update` with the session
  object, wait for `session.updated`; audio as `input_audio_buffer.append` (`audio` = base64
  PCM16), `input_audio_buffer.commit` at the end of each utterance.
- Direct: `POST {base}/realtime/client_secrets` with the platform key, body
  `{"expires_after": {"anchor": "created_at", "seconds": 120}, "session": <session object>}` →
  `{"value": "ek_…", "expires_at", "session": {"id"}}`; then `POST {base}/realtime/calls` with the
  SDP offer, `Authorization: Bearer <client secret>`, `Content-Type: application/sdp`; events on
  the data channel `oai-events`.
- Events used: `conversation.item.input_audio_transcription.delta` (`item_id`, `delta`),
  `…completed` (`item_id`, `transcript`), `…failed`, `input_audio_buffer.committed` (`item_id`),
  `error`. Completions of different turns may arrive out of order: matched by `item_id`.

## Server

| Route | Who | What |
| --- | --- | --- |
| `GET /api/admin/voice` | admin | settings without the key (`key_set`, `key_last4`, `usable`, `transport`) |
| `PUT /api/admin/voice` | admin | save; `api_key` is write-only (sealed with the SecretBox), empty keeps it, `clear_key` removes it; enabling needs a key; changing `base_url` while a key is saved needs the key typed again (422); `transport` `relay` (default) or `webrtc` |
| `POST /api/admin/voice/test` | admin | mints a 10 s client secret with the typed or saved key (10/min); with the saved key `base_url` must equal the saved one |
| `GET /api/voice/config` | user | `{enabled, provider, transport, languages}`; disabled/unusable → `enabled: false`, no mic anywhere |
| `GET /api/voice/stream` | user | the relay WebSocket (below) |
| `POST /api/voice/session` | user | direct mode: `{language}` → `{client_secret, expires_at, connect_url, transport: "webrtc", model, client_commits}`; 20/min per user |

### Relay (`internal/voice/relay.go`, `internal/httpapi/voice_stream.go`)

- Auth: the session cookie, and the CSRF token as the subprotocol `csrf.<agent_teams_csrf>` next
  to `agent-teams-voice` (browsers can't set headers on a WebSocket; the token stays out of URLs
  and logs). coder/websocket's Origin check rejects cross-site pages. 401 / 403 / 422 (language)
  are answered before the upgrade; everything after it is a fatal JSON message plus a close, so
  the browser can show the reason.
- Limits: 20 streams per user per minute; 2 at the same time (Valkey slot keys, shared by all
  console replicas, expiring after 6 minutes); 5 minutes per recording; 30 s without a message
  from the browser; 64 KiB per message; audio may not run more than 10 s ahead of real time;
  10 s to dial OpenAI and get `session.updated`.
- Browser → server: binary frames of 24 kHz mono PCM16 little-endian (≈100 ms), `{"type":"commit"}`,
  `{"type":"stop"}`. Server → browser: `{"type":"ready","model","client_commits","max_seconds"}`,
  then OpenAI's transcript events reduced to `type/item_id/delta/transcript/error{code,message}`
  (anything else is dropped; the key is replaced by `***` in messages), and
  `{"type":"error","fatal":true,"error":{"code","message"}}` before it closes.
- Never logged: audio, transcripts, the key. Audit: `voice_stream_start`, `voice_stream_stop`
  (`seconds`, `audio_seconds`, `commits`, `reason`, OpenAI session id as the target).

Settings live in `at_settings["voice"]`. The saved key is only ever sent to the base URL it was
saved with, so a changed address can't redirect it to another host; an upstream error message is
stripped of the key before it is logged or returned. Other audit actions: `voice_settings_change`,
`voice_test`, `voice_session` (never the key or the secret). Direct-mode mint errors map to 502
`voice_key_rejected` / `voice_upstream` / `voice_unreachable`, 503 `voice_busy`.

## Web

`lib/voice/`: `controller.ts` (state machine idle → connecting → recording → stopping, transcript,
client VAD), `relay.ts` (default transport), `pcm.ts` (AudioWorklet capture, `Resampler` to
24 kHz with a box filter, `toPcm16`, `Framer` for 100 ms frames), `openai.ts` (shared types and
the WebRTC transport), `audio.ts` (microphone, device list, level meter), `transcript.ts`.
`components/app/voice/`: `VoiceButton`, `VoiceMenu`, `VoiceOverlay`, `useVoiceInput`.

- Mic button: click toggles (with 按住录音 the button or Alt+M/⌥M is held). While active it is a
  blue mic on a light-blue circle and a click always ends it: cancels while connecting (spinner
  ring, "正在连接…点击取消"), stops while recording ("点击停止"), stops waiting for the last words
  while stopping. Esc stops.
- Speech is inserted at the cursor (replacing a selection); partial text is grey until its
  `completed` event; the box is read-only while recording; sending stops first and waits up to
  4 s for the last transcript. Client VAD: 800 ms of quiet after speech, or 20 s of talking,
  commits; stopping commits once more and waits for that commit's transcript. With the relay the
  buffered audio is flushed before each commit and `stop` is sent after it. The controller stops
  by itself 3 s before the server's `max_seconds`.
- Timeouts: connecting (mint + connect) fails after 10 s with "连不上语音服务：…" instead of
  spinning; the relay rejects with the server's reason (e.g. "连不上语音服务：OpenAI 拒绝了 API
  Key"); direct mode aborts the SDP `fetch` and the data-channel wait at the same deadline.
- Menu: 麦克风 rows with exactly one checked (Chrome's `default` pseudo device becomes
  "系统默认（<name>）", `communications` is dropped, duplicates merged by group and name; a saved
  microphone that is gone falls back to 系统默认; before permission the rows are "麦克风 N"
  placeholders with 允许; refreshed on `devicechange`), a 语言 row that opens a searchable list
  in place, and 按住录音 with a switch. Preferences: `localStorage["voice-prefs:<user id>"]`.
- Dev: the Vite proxy forwards WebSockets (`ws: true`), so the relay works under `npm run dev`.

## Verification

- Go: `internal/httpapi/voice_stream_test.go` against a fake OpenAI WebSocket (key, URL,
  safety id, session.update body, audio bytes, events reduced and key-free, CSRF/Origin/auth,
  limits, idle, max duration, silent upstream, rejected key, audit); `internal/voice/relay_test.go`.
- Web: `src/__tests__/voice-relay.test.ts` (resampling/framing math, relay with a fake socket,
  timeouts, cancel while connecting, device list, menu, button) and `voice.test.ts`.
- Real browser, locally: Chromium with a fake microphone (440 Hz tone bursts) → Vite → atctl →
  `/api/voice/stream` → fake OpenAI; three utterances committed by the client VAD, the fake
  measured 24 kHz audio at the right pitch and the transcripts landed in the coordinator composer;
  with OpenAI down the button showed "连不上语音服务：服务器连不上 OpenAI" within 0.3 s.
- Not yet run against real OpenAI over the relay: production check script in the commit that
  added it (connect with the key, `session.update`, expect `session.updated`, send 1 s of audio,
  commit, expect `input_audio_buffer.committed`). If OpenAI answers the transcription update
  differently than documented, the relay reports `voice_timeout` within 10 s.
