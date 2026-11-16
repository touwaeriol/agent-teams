# Voice input (语音输入)

The composers (coordinator chat and thread lead) get a mic button when an administrator has
configured a speech-to-text provider in 管理 → 语音输入. Every signed-in user can use it. Audio goes
from the browser straight to OpenAI; the control plane only mints short-lived client secrets.

## Provider: OpenAI Realtime transcription

Checked against developers.openai.com on 2026-11-16:

- Model: `gpt-realtime-whisper` ("a streaming speech-to-text model for applications that need
  low-latency transcript deltas from live audio"; realtime transcription sessions only, $0.017/min).
  The admin can set another model id (`gpt-live-transcribe`, `gpt-4o-transcribe`, …).
- Ephemeral key: `POST {base}/realtime/client_secrets` with the platform key,
  body `{"expires_after": {"anchor": "created_at", "seconds": 120}, "session": {"type": "transcription",
  "audio": {"input": {"format": {"type": "audio/pcm", "rate": 24000}, "noise_reduction": {"type": "near_field"} | null,
  "transcription": {"model", "language"?, "delay"?}, "turn_detection": null}}}}`; response
  `{"value": "ek_…", "expires_at": <unix s>, "session": {"id": "sess_…"}}`.
  - `gpt-realtime-whisper`: turn detection must be `null` (no VAD), `prompt` is not supported,
    `delay` (`minimal|low|medium|high|xhigh`) is supported only by this model.
  - `gpt-live-transcribe` / `gpt-transcribe` take `languages: [code]` instead of `language`; older
    transcribe models get `server_vad`.
- Browser connection: WebRTC. `POST {base}/realtime/calls` with the SDP offer,
  `Authorization: Bearer <client secret>`, `Content-Type: application/sdp`; events on the data
  channel `oai-events`.
- Events used: `conversation.item.input_audio_transcription.delta` (`item_id`, `delta`),
  `…completed` (`item_id`, `transcript`), `…failed`, `input_audio_buffer.committed` (`item_id`),
  `error`. Without server VAD the client sends `input_audio_buffer.commit` at the end of each
  utterance (OpenAI: "use client-side VAD … then send input_audio_buffer.commit").

## Server

| Route | Who | What |
| --- | --- | --- |
| `GET /api/admin/voice` | admin | settings without the key (`key_set`, `key_last4`, `usable`) |
| `PUT /api/admin/voice` | admin | save; `api_key` is write-only (sealed with the SecretBox), empty keeps it, `clear_key` removes it; enabling needs a key; changing `base_url` while a key is saved needs the key typed again (422) |
| `POST /api/admin/voice/test` | admin | mints a 10 s client secret with the typed or saved key (10/min); with the saved key `base_url` must equal the saved one |
| `GET /api/voice/config` | user | `{enabled, provider, languages}`; disabled/unusable → `enabled: false`, no mic anywhere |
| `POST /api/voice/session` | user | `{language}` (`auto` or a listed code) → `{client_secret, expires_at, connect_url, transport: "webrtc", model, client_commits}`; 20/min per user |

Settings live in `at_settings["voice"]`. The saved key is only ever sent to the base URL it was
saved with, so a changed address can't redirect it to another host; an upstream error message is
stripped of the key before it is logged or returned. Audit actions: `voice_settings_change`, `voice_test`,
`voice_session` (never the key or the secret). OpenAI errors map to 502 `voice_key_rejected` /
`voice_upstream` / `voice_unreachable`, 503 `voice_busy`.

## Web

`components/app/voice/`: `VoiceButton` (mic + chevron menu: 麦克风, 语言 with search, 按住录音),
`VoiceOverlay` (grey partial text over the field), `useVoiceInput` (wires `lib/voice/controller.ts`
to the composer). Click toggles; with 按住录音 the button or Alt+M (⌥M) is held; Esc stops.
Speech is inserted at the cursor (replacing a selection); partial text is grey until its
`completed` event; the box is read-only while recording; sending stops first and waits up to 4 s
for the last transcript. Client VAD: 800 ms of quiet after speech, or 20 s of talking, commits.
A rejected secret (401/403 on connect) or one that already expired is reminted once.
Preferences: `localStorage["voice-prefs:<user id>"]`.

## Not yet verified against OpenAI

There was no OpenAI key when this was built. Verified with fakes: the client secret request body
and headers (Go fake server), the browser's SDP POST (URL, `Bearer ek_…`, `application/sdp`) and
the event handling (Playwright with a fake RTCPeerConnection). To check with a real key:

1. 管理 → 语音输入 → 测试 succeeds (OpenAI accepts the transcription session body above for
   `gpt-realtime-whisper`, including `delay` and `noise_reduction`).
2. `/realtime/calls` accepts a transcription-session secret over WebRTC (the WebRTC guide only
   shows `type: "realtime"`), and the answer SDP brings up the `oai-events` channel.
3. Deltas arrive before a commit and `completed` follows each `input_audio_buffer.commit`; tune
   `SPEECH_LEVEL` / `SILENCE_MS` in `lib/voice/controller.ts` on real microphones.
4. How long a connected session lasts after the 120 s secret expires (recordings longer than that).
