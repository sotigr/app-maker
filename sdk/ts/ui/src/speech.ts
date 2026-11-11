// Dictation (ADR 0028): which fields accept it, and the two engines. The
// browser engine uses the Web Speech API in the user's UI language; the
// server engine records audio and posts it to /v1/ui/transcribe. One shared
// microphone (components/speech.tsx) dictates into the focused field.
import { useEffect, useRef } from "react";
import type { OrganonClient } from "@organon/client";
import { speechBase } from "./speechtext";

export interface SpeechConfig {
  mode: "auto" | "browser" | "server";
  maxSeconds: number;
  position: "end" | "start";
  /** server: the server transcribes for this caller. */
  server: boolean;
}

type Editable = HTMLInputElement | HTMLTextAreaElement;

/** A field that accepts dictation. */
export interface DictationTarget {
  el: Editable;
  label: string;
  get(): string;
  set(value: string): void;
}

const targets = new Map<Element, { current: DictationTarget }>();
const listeners = new Set<() => void>();
let last: Element | null = null;

function changed() {
  listeners.forEach((l) => l());
}

/** onTargetsChange is called when the focused dictation target changes. */
export function onTargetsChange(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/**
 * useDictationTarget registers an input or textarea for dictation while
 * opts is not null; the shared microphone inserts text through opts.set,
 * so form state and validation see it like typing.
 */
export function useDictationTarget(ref: { current: Editable | null }, opts: Omit<DictationTarget, "el"> | null): void {
  // The latest options, so the microphone always calls this render's set.
  const latest = useRef(opts);
  latest.current = opts;
  const on = opts !== null;
  useEffect(() => {
    const el = ref.current;
    if (!on || !el) return;
    const box = {
      get current(): DictationTarget {
        const o = latest.current;
        return o ? { el, ...o } : { el, label: "", get: () => el.value, set: () => undefined };
      },
    };
    targets.set(el, box);
    el.dataset.oSpeech = "";
    const focus = () => {
      last = el;
      changed();
    };
    el.addEventListener("focus", focus);
    el.addEventListener("blur", changed);
    return () => {
      el.removeEventListener("focus", focus);
      el.removeEventListener("blur", changed);
      targets.delete(el);
      delete el.dataset.oSpeech;
      if (last === el) last = null;
      changed();
    };
  }, [on, ref.current]);
}

/**
 * currentTarget is the focused dictation target, or the last one focused
 * when focus moved to something that is not an editable field (the
 * microphone on touch screens).
 */
export function currentTarget(): DictationTarget | undefined {
  const active = typeof document !== "undefined" ? document.activeElement : null;
  if (active && targets.has(active)) return targets.get(active)!.current;
  const editable = active && (active.tagName === "INPUT" || active.tagName === "TEXTAREA" || active.tagName === "SELECT");
  if (!editable && last && last.isConnected && targets.has(last)) return targets.get(last)!.current;
  return undefined;
}

// ---- Engines -----------------------------------------------------------

export type Engine = "browser" | "server";

/** browserBroken is set when browser recognition is refused at run time
 * (no speech service, e.g. Brave); later dictation uses the server. */
let browserBroken = false;

function recognitionClass(): (new () => any) | undefined {
  const w = globalThis as any;
  return w.SpeechRecognition || w.webkitSpeechRecognition;
}

/** pickEngine chooses the engine for the app's mode, or null for none. */
export function pickEngine(cfg: SpeechConfig): Engine | null {
  const nav = (globalThis as any).navigator;
  const browserOK = !!recognitionClass() && !nav?.brave && !browserBroken;
  const serverOK = cfg.server && typeof MediaRecorder !== "undefined" && !!nav?.mediaDevices?.getUserMedia;
  if (cfg.mode === "browser") return browserOK ? "browser" : null;
  if (cfg.mode === "server") return serverOK ? "server" : null;
  return browserOK ? "browser" : serverOK ? "server" : null;
}

/** Why dictation stopped with an error. */
export type SpeechError = "denied" | "unavailable" | "failed";

export interface SessionEvents {
  /** interim is the words heard so far (browser engine). */
  interim(text: string): void;
  /** final is text to insert; the browser engine sends several. */
  final(text: string): void;
  /** busy: the recording is being transcribed (server engine). */
  busy(): void;
  error(e: SpeechError): void;
  /** end: the session is over (after final, error, or cancel). */
  end(): void;
}

export interface Session {
  /** stop ends listening and inserts what was recognized. */
  stop(): void;
  /** cancel ends listening and drops the recording. */
  cancel(): void;
}

/** startBrowser listens with the Web Speech API in lang (BCP 47). */
export function startBrowser(lang: string, maxSeconds: number, on: SessionEvents): Session {
  const SR = recognitionClass()!;
  const rec = new SR();
  rec.lang = lang;
  rec.continuous = true;
  rec.interimResults = true;
  let cancelled = false;
  let failed = false;
  rec.onresult = (e: any) => {
    let interim = "";
    for (let i = e.resultIndex; i < e.results.length; i++) {
      const r = e.results[i];
      if (r.isFinal) on.final(r[0].transcript);
      else interim += r[0].transcript;
    }
    on.interim(interim);
  };
  rec.onerror = (e: any) => {
    if (cancelled || e.error === "aborted" || e.error === "no-speech") return;
    failed = true;
    if (e.error === "service-not-allowed" || e.error === "network" || e.error === "language-not-supported") {
      browserBroken = true;
      on.error("unavailable");
    } else on.error(e.error === "not-allowed" ? "denied" : "failed");
  };
  rec.onend = () => {
    clearTimeout(limit);
    if (!failed || cancelled) on.interim("");
    on.end();
  };
  const limit = setTimeout(() => rec.stop(), maxSeconds * 1000);
  rec.start();
  return {
    stop: () => rec.stop(),
    cancel: () => {
      cancelled = true;
      rec.abort();
    },
  };
}

const RECORD_TYPES = ["audio/webm;codecs=opus", "audio/ogg;codecs=opus", "audio/mp4", "audio/webm"];

/**
 * startServer records the microphone (Opus at 32 kbit/s) until stop, the
 * app's maxSeconds, or 2.5 s of silence after speech, then transcribes the
 * recording on the server in lang.
 */
export async function startServer(client: OrganonClient, lang: string, maxSeconds: number, on: SessionEvents): Promise<Session> {
  let stream: MediaStream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true });
  } catch (e) {
    const name = (e as { name?: string }).name;
    on.error(name === "NotAllowedError" || name === "SecurityError" ? "denied" : "unavailable");
    on.end();
    return { stop: () => undefined, cancel: () => undefined };
  }
  const mimeType = RECORD_TYPES.find((t) => MediaRecorder.isTypeSupported?.(t));
  const rec = new MediaRecorder(stream, { ...(mimeType ? { mimeType } : {}), audioBitsPerSecond: 32000 });
  const chunks: Blob[] = [];
  let cancelled = false;
  rec.ondataavailable = (e) => e.data.size && chunks.push(e.data);

  // Silence detection: stop 2.5 s after the last speech-level sound.
  let ctx: AudioContext | undefined;
  let watch: ReturnType<typeof setInterval> | undefined;
  try {
    ctx = new AudioContext();
    const analyser = ctx.createAnalyser();
    analyser.fftSize = 1024;
    ctx.createMediaStreamSource(stream).connect(analyser);
    const buf = new Float32Array(analyser.fftSize);
    let heard = false;
    let quietSince = performance.now();
    watch = setInterval(() => {
      analyser.getFloatTimeDomainData(buf);
      let sum = 0;
      for (const v of buf) sum += v * v;
      const loud = Math.sqrt(sum / buf.length) > 0.02;
      const now = performance.now();
      if (loud) {
        heard = true;
        quietSince = now;
      } else if (heard && now - quietSince > 2500 && rec.state === "recording") rec.stop();
    }, 200);
  } catch {
    // No Web Audio: stop by click or time limit only.
  }
  const limit = setTimeout(() => rec.state === "recording" && rec.stop(), maxSeconds * 1000);
  const release = () => {
    clearTimeout(limit);
    if (watch) clearInterval(watch);
    stream.getTracks().forEach((t) => t.stop());
    ctx?.close().catch(() => undefined);
  };
  rec.onstop = async () => {
    release();
    if (cancelled || chunks.length === 0) {
      on.end();
      return;
    }
    on.busy();
    try {
      const r = await client.transcribe(new Blob(chunks, { type: rec.mimeType || mimeType || "audio/webm" }), speechBase(lang));
      on.final(r.text);
    } catch (e) {
      on.error((e as { status?: number }).status === 503 ? "unavailable" : "failed");
    }
    on.end();
  };
  rec.start(250);
  return {
    stop: () => rec.state === "recording" && rec.stop(),
    cancel: () => {
      cancelled = true;
      if (rec.state === "recording") rec.stop();
      else release();
    },
  };
}
