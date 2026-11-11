// MicDock is the one shared microphone of the page (ADR 0028): it dictates
// into the focused text field, in the user's UI language. Ctrl/Cmd+Shift+
// Space starts and stops it; Escape cancels.
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { useRuntime } from "../runtime";
import { currentLocale } from "../i18n";
import { currentTarget, onTargetsChange, pickEngine, startBrowser, startServer, type DictationTarget, type Session, type SpeechConfig, type SpeechError } from "../speech";
import { insertAt, speechLang } from "../speechtext";

type Phase = "idle" | "starting" | "listening" | "busy";

export function MicDock({ cfg }: { cfg: SpeechConfig }) {
  const rt = useRuntime();
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  const [phase, setPhase] = useState<Phase>("idle");
  const [interim, setInterim] = useState("");
  const [error, setError] = useState("");
  const session = useRef<Session | null>(null);
  const target = useRef<DictationTarget | null>(null);
  const caret = useRef({ start: 0, end: 0 });

  useEffect(() => onTargetsChange(rerender), []);
  // Moving to another field stops dictation and keeps what was heard.
  useEffect(() => onTargetsChange(() => {
    const t = currentTarget();
    if (session.current && target.current && t && t.el !== target.current.el) session.current.stop();
  }), []);

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
    setError(e === "denied" ? rt.t("Microphone access was denied.")
      : e === "unavailable" ? rt.t("Speech recognition is not available.") : rt.t("Speech recognition failed."));
    setTimeout(() => setError(""), 6000);
  }, [rt]);

  const start = useCallback(async () => {
    const t = currentTarget();
    const engine = pickEngine(cfg);
    if (!t || !engine || session.current) return;
    target.current = t;
    caret.current = { start: t.el.selectionStart ?? t.get().length, end: t.el.selectionEnd ?? t.get().length };
    t.el.classList.add("o-dictating");
    setError("");
    setPhase("starting");
    const lang = speechLang(currentLocale(), typeof navigator !== "undefined" ? navigator.languages : []);
    const events = {
      interim: setInterim,
      final: insert,
      busy: () => setPhase("busy"),
      error: (e: SpeechError) => {
        fail(e);
        // Browser recognition refused (no speech service): try the server.
        if (e === "unavailable" && engine === "browser" && pickEngine(cfg) === "server") {
          setError("");
          retry = true;
        }
      },
      end: () => {
        if (retry) {
          retry = false;
          session.current = null;
          setPhase("starting");
          startServer(rt.client, lang, cfg.maxSeconds, events).then((s) => { session.current = s; setPhase((p) => (p === "starting" ? "listening" : p)); });
          return;
        }
        finish();
      },
    };
    let retry = false;
    if (engine === "browser") {
      session.current = startBrowser(lang, cfg.maxSeconds, events);
      setPhase("listening");
    } else {
      const s = await startServer(rt.client, lang, cfg.maxSeconds, events);
      session.current = s;
      setPhase((p) => (p === "starting" ? "listening" : p));
    }
  }, [cfg, rt, insert, fail, finish]);

  const toggle = useCallback(() => {
    if (session.current) session.current.stop();
    else start();
  }, [start]);

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.code === "Space") {
        e.preventDefault();
        toggle();
      } else if (e.key === "Escape" && session.current) {
        session.current.cancel();
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [toggle]);

  // Without an engine (browser mode in Firefox or Brave, or no server
  // transcriber) the button stays, disabled, and says why.
  const supported = !!pickEngine(cfg);
  const t = currentTarget();
  const active = phase !== "idle";
  const ready = (supported && !!t) || active;
  const label = active ? rt.t("Stop dictation") : !supported ? rt.t("Speech recognition is not available.")
    : ready ? rt.t("Dictate") : rt.t("Click a text field, then dictate");
  // Focusing a field where dictation is impossible explains it once there.
  const bubble = error || (!supported && !active && t ? rt.t("Speech recognition is not available.") : "") || (phase === "busy" ? rt.t("Transcribing…")
    : active ? `${rt.t("Listening…")}${target.current?.label ? ` (${target.current.label})` : ""}${interim ? ` ${interim}` : ""}` : "");
  return (
    <div className={`o-mic-dock o-mic-${cfg.position || "end"}`}>
      <div className={`o-mic-bubble${bubble ? "" : " o-hidden"}${error ? " o-mic-error" : ""}`} aria-live="polite">{bubble}</div>
      <button type="button" className={`o-mic${phase === "listening" || phase === "starting" ? " o-mic-on" : ""}${phase === "busy" ? " o-mic-busy" : ""}`}
        aria-pressed={active} aria-label={label} title={`${label} (Ctrl+Shift+Space)`} disabled={!ready || phase === "busy"}
        // Keep the text field focused: the click must not take focus away.
        onMouseDown={(e) => e.preventDefault()} onClick={toggle}>
        <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true">
          <path fill="currentColor" d="M12 15a3 3 0 0 0 3-3V6a3 3 0 1 0-6 0v6a3 3 0 0 0 3 3Zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.92V22h2v-3.08A7 7 0 0 0 19 12h-2Z" />
        </svg>
      </button>
    </div>
  );
}
