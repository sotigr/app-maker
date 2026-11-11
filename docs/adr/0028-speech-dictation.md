# 0028: Speech dictation in UI text fields

Status: accepted (2026-11-09)

## Context
Users of Organon apps, often on tablets at a counter or in a workshop,
want to speak into text fields instead of typing, in the language they
chose for the UI (Greek for Greek users), not the browser's default.
Decided with the user (2026-11-09): a hybrid engine, dictation into the
field (no AI rewriting), an app-level opt-in covering every text field
with per-field opt-out, and one shared microphone button rather than one
per field.

## Decision
- **Manifest:** `ui/app.json` `"speech": {"mode": "auto"|"browser"|"server",
  "maxSeconds": 5-120 (60), "position": "end"|"start"}` turns dictation
  on. Form `fields[]` items take `"speech": false`.
- **Renderer:**
  - One `MicDock` per page dictates into the focused eligible field.
    Ctrl/Cmd+Shift+Space starts and stops it; Escape cancels. Moving to
    another field stops dictation and keeps what was heard.
  - Fields register through `useDictationTarget`. Text is inserted through
    the field's own state setter at the cursor, spaced, and capitalized at
    a sentence start (`insertAt`). The Greek capital keeps its accent.
  - Eligible: form `text`/`textarea` widgets that are not email, URL, ID,
    or date fields, and reference-picker searches.
- **Language:** the bundle language (`currentLocale()`), widened to a
  regional tag for browser recognition (`el` → `el-GR`, or the browser's
  matching regional tag). The server receives the primary subtag; it uses
  the caller's resolved UI language when none is given.
- **Engines:**
  - The browser engine is the Web Speech API, continuous with interim
    results. It is skipped on Brave and after a "service not allowed" or
    "network" error, falling back to the server in `auto` mode.
  - The server engine records Opus at 32 kbit/s with `MediaRecorder` and
    stops on release or at `maxSeconds`. It posts to
    `POST /v1/ui/transcribe?lang=` (raw audio, at most 1 MiB, signed-in or
    link callers). That calls the `Transcriber` port (runtime context):
    an OpenAI-compatible `/audio/transcriptions` adapter (OpenAI Whisper,
    Groq, self-hosted whisper) or a fake (organon dev).
  - The server config is `speech: {kind, baseURL, apiKey: ${secret:...}, model}`
    or `ORGANON_SPEECH_*`. The bundle tells the renderer whether a server
    fallback exists for the caller.
- **Privacy:** audio and text are never stored or logged; only the
  duration metric is recorded. `server` mode keeps audio away from browser
  vendors. The renderer page sends `Permissions-Policy: microphone=(self)`.

## Consequences
- In `auto` mode without a configured transcriber, Firefox and Brave users
  see a disabled microphone that says recognition is not available (as
  do Firefox and Brave users in `browser` mode).
- Interim words show in the dock's bubble, not in the field; only final
  text enters the field, so form state never holds partial results.

## Amendment (2026-11-11): hold to talk
The microphone records while it is held down (pointer with capture, so
touch and mouse behave alike, or Ctrl/Cmd+Shift+Space, or Space/Enter on
the focused button) and inserts on release; a press under 300 ms is a tap,
records nothing, and shows "Hold the microphone while you speak." The
server engine's silence auto-stop was removed: the user decides the end.
A release that arrives before the server engine has the microphone stops
the recording as soon as it starts.
