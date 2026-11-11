// MicDock is the one shared microphone of the page (ADR 0028): it dictates
// into the focused text field, in the user's UI language, while it is held
// down (mouse, touch, or Ctrl/Cmd+Shift+Space); releasing it inserts the
// text. Escape cancels.
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { useRuntime } from "../runtime";
import { currentLocale } from "../i18n";
import { currentTarget, onTargetsChange, pickEngine, startBrowser, startServer, type DictationTarget, type Session, type SpeechConfig, type SpeechError } from "../speech";
import { insertAt, speechLang } from "../speechtext";

type Phase = "idle" | "starting" | "listening" | "busy";

/** A press shorter than this is a tap: nothing is recorded, a hint shows. */
const MIN_HOLD_MS = 300;

export function MicDock({ cfg }: { cfg: SpeechConfig }) {
  const rt = useRuntime();
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  const [phase, setPhase] = useState<Phase>("idle");
  const [interim, setInterim] = useState("");
  const [message, setMessage] = useState<{ text: string; error: boolean } | null>(null);
  const session = useRef<Session | null>(null);
  const target = useRef<DictationTarget | null>(null);
  const caret = useRef({ start: 0, end: 0 });
  // held: the microphone is pressed; pressedAt: when; after: what to do with
  // a session that starts after the release (the server engine is async).
  const held = useRef(false);
  const pressedAt = useRef(0);
  const after = useRef<"stop" | "cancel" | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => onTargetsChange(rerender), []);

  const show = useCallback((text: string, error: boolean) => {
    clearTimeout(timer.current);
    setMessage({ text, error });
    timer.current = setTimeout(() => setMessage(null), error ? 6000 : 3000);
  }, []);

  const finish = useCallback(() => {
    target.current?.el.classList.remove("o-dictating");
    session.current = null;
    setPhase("idle");
    setInterim("");
  }, []);

  const insert = useCallback((text: string) => {
    const t = target.current;
    if (!t) return;
    const r = insertAt(t.get(), caret.current.start, caret.current.end, text, currentLocale());
    t.set(r.value);
    caret.current = { start: r.caret, end: r.caret };
    requestAnimationFrame(() => {
      t.el.focus();
      t.el.setSelectionRange(r.caret, r.caret);
    });
  }, []);

  const fail = useCallback((e: SpeechError) => {
    show(e === "denied" ? rt.t("Microphone access was denied.")
      : e === "unavailable" ? rt.t("Speech recognition is not available.") : rt.t("Speech recognition failed."), true);
  }, [rt, show]);

  // adopt takes a started session; a release that came first stops it.
  const adopt = useCallback((s: Session) => {
    session.current = s;
    setPhase((p) => (p === "starting" ? "listening" : p));
    const a = after.current;
    after.current = null;
    if (a === "cancel") s.cancel();
    else if (a === "stop") s.stop();
  }, []);

  const start = useCallback(async () => {
    const t = currentTarget();
    const engine = pickEngine(cfg);
    if (!t || !engine || session.current) return;
    target.current = t;
    caret.current = { start: t.el.selectionStart ?? t.get().length, end: t.el.selectionEnd ?? t.get().length };
    t.el.classList.add("o-dictating");
    setMessage(null);
    setPhase("starting");
    const lang = speechLang(currentLocale(), typeof navigator !== "undefined" ? navigator.languages : []);
    let retry = false;
    const events = {
      interim: setInterim,
      final: insert,
      busy: () => setPhase("busy"),
      error: (e: SpeechError) => {
        // Browser recognition refused (no speech service): while the
        // microphone is still held, go on with the server; later presses
        // use the server directly.
        if (e === "unavailable" && engine === "browser" && pickEngine(cfg) === "server") {
          retry = held.current;
          return;
        }
        fail(e);
      },
      end: () => {
        if (retry) {
          retry = false;
          session.current = null;
          setPhase("starting");
          startServer(rt.client, lang, cfg.maxSeconds, events).then(adopt);
          return;
        }
        finish();
      },
    };
    if (engine === "browser") adopt(startBrowser(lang, cfg.maxSeconds, events));
    else adopt(await startServer(rt.client, lang, cfg.maxSeconds, events));
  }, [cfg, rt, insert, fail, finish, adopt]);

  const press = useCallback(() => {
    if (held.current || session.current || phase === "busy") return;
    held.current = true;
    pressedAt.current = Date.now();
    after.current = null;
    start();
  }, [start, phase]);

  const release = useCallback(() => {
    if (!held.current) return;
    held.current = false;
    const tap = Date.now() - pressedAt.current < MIN_HOLD_MS;
    if (tap) show(rt.t("Hold the microphone while you speak."), false);
    const action = tap ? "cancel" : "stop";
    if (session.current) session.current[action]();
    else after.current = action;
  }, [rt, show]);

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.code === "Space") {
        e.preventDefault();
        if (!e.repeat) press();
      } else if (e.key === "Escape" && (session.current || held.current)) {
        held.current = false;
        if (session.current) session.current.cancel();
        else after.current = "cancel";
      }
    };
    // Letting go of any key of the shortcut ends the hold.
    const up = (e: KeyboardEvent) => {
      if (held.current && (e.code === "Space" || e.key === "Shift" || e.key === "Control" || e.key === "Meta")) release();
    };
    // Switching windows while holding must not leave the microphone on.
    const blur = () => release();
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    window.addEventListener("blur", blur);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
      window.removeEventListener("blur", blur);
    };
  }, [press, release]);

  useEffect(() => () => clearTimeout(timer.current), []);

  // Without an engine (browser mode in Firefox or Brave, or no server
  // transcriber) the button stays, disabled, and says why.
  const supported = !!pickEngine(cfg);
  const t = currentTarget();
  const active = phase !== "idle";
  const ready = (supported && !!t) || active;
  const label = active ? rt.t("Release to stop") : !supported ? rt.t("Speech recognition is not available.")
    : ready ? rt.t("Hold to dictate") : rt.t("Click a text field, then dictate");
  // Focusing a field where dictation is impossible explains it once there.
  const bubble = message?.text || (!supported && !active && t ? rt.t("Speech recognition is not available.") : "") || (phase === "busy"
    ? rt.t("Transcribing…")
    : active ? `${rt.t("Listening…")}${target.current?.label ? ` (${target.current.label})` : ""}${interim ? ` ${interim}` : ""}` : "");
  return (
    <div className={`o-mic-dock o-mic-${cfg.position || "end"}`}>
      <div className={`o-mic-bubble${bubble ? "" : " o-hidden"}${message?.error ? " o-mic-error" : ""}`} aria-live="polite">{bubble}</div>
      <button type="button" className={`o-mic${phase === "listening" || phase === "starting" ? " o-mic-on" : ""}${phase === "busy" ? " o-mic-busy" : ""}`}
        aria-pressed={active} aria-label={label} title={`${label} (Ctrl+Shift+Space)`} disabled={!ready || phase === "busy"}
        // Hold to talk. preventDefault keeps the text field focused (and
        // stops touch long-press menus); the capture delivers the release
        // even when the pointer slides off the button.
        onPointerDown={(e) => {
          if (e.button !== 0) return;
          e.preventDefault();
          e.currentTarget.setPointerCapture?.(e.pointerId);
          press();
        }}
        onPointerUp={release} onPointerCancel={release} onLostPointerCapture={release}
        onContextMenu={(e) => e.preventDefault()}
        // Keyboard users on the button itself: hold Space or Enter.
        onKeyDown={(e) => { if ((e.key === " " || e.key === "Enter") && !e.repeat && !e.ctrlKey && !e.metaKey) { e.preventDefault(); press(); } }}
        onKeyUp={(e) => { if (e.key === " " || e.key === "Enter") release(); }}>
        <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true">
          <path fill="currentColor" d="M12 15a3 3 0 0 0 3-3V6a3 3 0 1 0-6 0v6a3 3 0 0 0 3 3Zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.92V22h2v-3.08A7 7 0 0 0 19 12h-2Z" />
        </svg>
      </button>
    </div>
  );
}
