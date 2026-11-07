// Shared console components (admin usability plan, phase 1): toasts,
// dialogs, confirmations, labelled fields, badges, empty and loading
// states. No dependencies beyond React.
import { createContext, ReactNode, useCallback, useContext, useEffect, useId, useRef, useState } from "react";
import { message } from "../api";

// ---- Toasts ----------------------------------------------------------

type Toast = { id: number; tone: "ok" | "error" | "info"; text: string };
type ToastApi = { ok: (text: string) => void; error: (e: unknown) => void; info: (text: string) => void };

const ToastCtx = createContext<ToastApi>({ ok: () => {}, error: () => {}, info: () => {} });

export function useToast(): ToastApi {
  return useContext(ToastCtx);
}

let toastSeq = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const push = useCallback((tone: Toast["tone"], text: string) => {
    const id = ++toastSeq;
    setToasts((t) => [...t, { id, tone, text }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === "error" ? 8000 : 4000);
  }, []);
  const [api] = useState<ToastApi>(() => ({
    ok: (text) => push("ok", text),
    info: (text) => push("info", text),
    error: (e) => push("error", message(e)),
  }));
  return (
    <ToastCtx.Provider value={api}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast toast-${t.tone}`}>
            <span>{t.text}</span>
            <button className="icon" aria-label="Dismiss" onClick={() => setToasts((x) => x.filter((y) => y.id !== t.id))}>×</button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

// ---- Dialogs ---------------------------------------------------------

const focusable = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Dialog is a modal: Escape and the backdrop close it, focus stays inside. */
export function Dialog({ title, onClose, children, footer, wide }: {
  title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    const first = ref.current?.querySelector<HTMLElement>("[autofocus]") || ref.current?.querySelector<HTMLElement>(focusable);
    first?.focus();
    return () => before?.focus?.();
  }, []);
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
    }
    if (e.key !== "Tab" || !ref.current) return;
    const items = [...ref.current.querySelectorAll<HTMLElement>(focusable)];
    if (items.length === 0) return;
    const [first, last] = [items[0], items[items.length - 1]];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };
  return (
    <div className="backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={ref} role="dialog" aria-modal="true" aria-labelledby={titleId} className={`dialog${wide ? " wide" : ""}`} onKeyDown={onKey}>
        <div className="dialog-head">
          <h2 id={titleId}>{title}</h2>
          <button className="icon" aria-label="Close" onClick={onClose}>×</button>
        </div>
        <div className="dialog-body">{children}</div>
        {footer && <div className="dialog-foot">{footer}</div>}
      </div>
    </div>
  );
}

type ConfirmOptions = { title: string; body?: ReactNode; confirm?: string; danger?: boolean };
const ConfirmCtx = createContext<(o: ConfirmOptions) => Promise<boolean>>(() => Promise.resolve(false));

/** useConfirm replaces window.confirm with a dialog: await confirm({...}). */
export function useConfirm() {
  return useContext(ConfirmCtx);
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<(ConfirmOptions & { resolve: (ok: boolean) => void }) | null>(null);
  const ask = useCallback((o: ConfirmOptions) => new Promise<boolean>((resolve) => setPending({ ...o, resolve })), []);
  const done = (ok: boolean) => {
    pending?.resolve(ok);
    setPending(null);
  };
  return (
    <ConfirmCtx.Provider value={ask}>
      {children}
      {pending && (
        <Dialog title={pending.title} onClose={() => done(false)}
          footer={<>
            <button onClick={() => done(false)}>Cancel</button>
            <button className={pending.danger ? "danger solid" : "primary"} autoFocus onClick={() => done(true)}>{pending.confirm || "Confirm"}</button>
          </>}>
          {pending.body}
        </Dialog>
      )}
    </ConfirmCtx.Provider>
  );
}

// ---- Fields ----------------------------------------------------------

export type FieldControl = { id: string; "aria-invalid"?: boolean; "aria-describedby"?: string };

/**
 * Field labels a control and shows its help and error text; children get
 * the props that tie the control to them.
 */
export function Field({ label, help, error, required, children }: {
  label: ReactNode; help?: ReactNode; error?: string; required?: boolean; children: (p: FieldControl) => ReactNode;
}) {
  const id = useId();
  const described = [help ? `${id}-help` : "", error ? `${id}-err` : ""].filter(Boolean).join(" ");
  return (
    <div className={`field${error ? " invalid" : ""}`}>
      <label htmlFor={id}>{label}{required && <span className="req" aria-hidden="true"> *</span>}</label>
      {children({ id, "aria-invalid": error ? true : undefined, "aria-describedby": described || undefined })}
      {help && <div id={`${id}-help`} className="help">{help}</div>}
      {error && <div id={`${id}-err`} className="field-error">{error}</div>}
    </div>
  );
}

// ---- Small pieces ----------------------------------------------------

const tones: Record<string, string> = {
  active: "ok", done: "ok", succeeded: "ok", success: "ok", applied: "ok", completed: "ok",
  suspended: "warn", pending: "warn", running: "info", queued: "info", retrying: "warn",
  failed: "bad", dead: "bad", deleted: "bad", error: "bad", cancelled: "muted",
};

export function Badge({ children, tone }: { children: ReactNode; tone?: string }) {
  const t = tone || tones[String(children)] || "muted";
  return <span className={`badge badge-${t}`}>{children}</span>;
}

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <strong>{title}</strong>
      {children && <div className="muted">{children}</div>}
    </div>
  );
}

export function Spinner({ label = "Loading…" }: { label?: string }) {
  return <div className="spinner" role="status"><span className="spin" aria-hidden="true" />{label}</div>;
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button type="button" className="small-btn" onClick={() => navigator.clipboard?.writeText(text).then(() => {
      setDone(true);
      setTimeout(() => setDone(false), 1500);
    })}>{done ? "Copied" : label}</button>
  );
}

/** Secret shows a value once (a new key or password) with a copy button. */
export function Secret({ value, note }: { value: string; note: string }) {
  return (
    <div className="secret">
      <code>{value}</code>
      <CopyButton text={value} />
      <div className="help">{note}</div>
    </div>
  );
}

export function formatTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}
