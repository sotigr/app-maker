// Contexts shared by every rendered node: the runtime (client, bundle,
// navigation, permissions), the page (state, dialogs), and the expression
// scope (params, data, state, item, ...).
import { createContext, useContext, useMemo } from "react";
import type { Feature, OrganonClient } from "@organon/client";
import { evaluate, template, type Env, type Funcs } from "./expr";
import { currentLocale, type Translate } from "./i18n";
import type { Bundle, Expr, Principal, Row, TypeMeta } from "./types";

/** Hooks the visual builder uses to select nodes in the preview. */
export interface EditorHooks {
  selected?: string;
  onSelect(path: string): void;
  /** Problems by node pointer, highlighted in the preview. */
  problems?: Set<string>;
}

export interface Runtime {
  client: OrganonClient;
  bundle: Bundle;
  principal: Principal | null;
  navigate(page: string, params?: Record<string, unknown>): void;
  href(page: string, params?: Record<string, unknown>): string;
  back(): void;
  toast(text: string, tone?: string): void;
  logout(): void;
  /** can answers a permission question from the batched cache; false until known. */
  can(f: unknown, op: string): boolean;
  /** label returns a display name for a feature ID (refs), loading it once. */
  label(id: string): string | undefined;
  /** t translates a text of the app, a page, or the renderer. */
  t: Translate;
  /** views returns the caller's create/update view schemas of a type. */
  views(type: string): Promise<{ create: ViewSchema; update: ViewSchema }>;
  editor?: EditorHooks;
}

export interface ViewSchema {
  properties?: Record<string, { type?: string | string[]; enum?: unknown[]; maxLength?: number; title?: string; description?: string; format?: string }>;
  required?: string[];
}

export interface PageState {
  name: string;
  state: Record<string, unknown>;
  setState(name: string, v: unknown): void;
  dialog?: string;
  openDialog(name: string): void;
  closeDialog(): void;
}

export const RuntimeContext = createContext<Runtime | null>(null);
export const PageContext = createContext<PageState | null>(null);
export const ScopeContext = createContext<Env>({});

export function useRuntime(): Runtime {
  const rt = useContext(RuntimeContext);
  if (!rt) throw new Error("@organon/ui: no runtime");
  return rt;
}

export function usePage(): PageState {
  const p = useContext(PageContext);
  if (!p) throw new Error("@organon/ui: no page");
  return p;
}

export function useScope(): Env {
  return useContext(ScopeContext);
}

export function typeMeta(rt: Runtime, type: string | undefined): TypeMeta | undefined {
  return type ? rt.bundle.types[type] : undefined;
}

/** flatten turns an API feature into the shape expressions see. */
export function flatten(f: Feature | undefined): Row | null {
  if (!f) return null;
  return {
    createdAt: f.createdAt,
    updatedAt: f.updatedAt,
    ...(f.data as Record<string, unknown>),
    id: f.id,
    type: f.type,
    parentId: f.parent ?? null,
    version: f.version,
  };
}

export function formatDate(v: unknown, style: unknown = "date"): string {
  if (v == null || v === "") return "";
  const d = v instanceof Date ? v : new Date(String(v));
  if (isNaN(d.getTime())) return String(v);
  switch (style) {
    case "datetime":
      return d.toLocaleString(currentLocale(), { dateStyle: "medium", timeStyle: "short" });
    case "time":
      return d.toLocaleTimeString(currentLocale(), { timeStyle: "short" });
    case "relative": {
      const s = (d.getTime() - Date.now()) / 1000;
      const fmt = new Intl.RelativeTimeFormat(currentLocale(), { numeric: "auto" });
      const units: [Intl.RelativeTimeFormatUnit, number][] = [["year", 31536000], ["month", 2592000], ["day", 86400], ["hour", 3600], ["minute", 60]];
      for (const [u, n] of units) if (Math.abs(s) >= n) return fmt.format(Math.round(s / n), u);
      return fmt.format(Math.round(s), "second");
    }
  }
  return d.toLocaleDateString(currentLocale(), { dateStyle: "medium" });
}

export function formatNumber(v: unknown, digits: unknown = 0): string {
  const n = typeof v === "number" ? v : Number(v);
  if (v == null || isNaN(n)) return "";
  const d = typeof digits === "number" ? digits : 0;
  return n.toLocaleString(currentLocale(), { minimumFractionDigits: d, maximumFractionDigits: d });
}

export function useFuncs(): Funcs {
  const rt = useRuntime();
  return useMemo(() => ({ can: (f, op) => rt.can(f, String(op)), formatDate, formatNumber }), [rt]);
}

const warned = new Set<string>();
function warn(e: unknown, src: string) {
  if (warned.has(src)) return;
  warned.add(src);
  console.warn(`@organon/ui: ${src}: ${e instanceof Error ? e.message : e}`);
}

/** Evaluator bound to the current scope. */
export interface Eval {
  env: Env;
  /** ex evaluates an expression prop; errors and nil give undefined. */
  ex(v: Expr | unknown): unknown;
  /** text renders a text prop, translated. */
  text(v: unknown): string;
  /** plain renders a text prop that is not translated (URLs). */
  plain(v: unknown): string;
}

export function makeEval(env: Env, fns: Funcs, tr: (text: string) => string = (s) => s): Eval {
  return {
    env,
    ex(v) {
      if (v === undefined) return undefined;
      if (typeof v !== "string") return v;
      try {
        return evaluate(v, env, fns) ?? undefined;
      } catch (e) {
        warn(e, v);
        return undefined;
      }
    },
    text(v) {
      return typeof v === "string" ? template(tr(v), env, fns, warn) : "";
    },
    plain(v) {
      return typeof v === "string" ? template(v, env, fns, warn) : "";
    },
  };
}

export function useEval(): Eval {
  const env = useScope();
  const fns = useFuncs();
  const rt = useRuntime();
  const page = useContext(PageContext)?.name;
  return useMemo(() => makeEval(env, fns, (s) => rt.t(s, page)), [env, fns, rt, page]);
}

/** enumLabel shows an enum value: its translation, else the value with spaces. */
export function enumLabel(meta: { labels?: Record<string, string> } | undefined, v: unknown): string {
  return meta?.labels?.[String(v)] ?? String(v).replace(/_/g, " ");
}

/** Route patterns ("/projects/:id") to URLs and back. */
export function buildPath(route: string, params: Record<string, unknown> = {}, declared: Record<string, unknown> = {}): string {
  const used = new Set<string>();
  const path = route.replace(/:([A-Za-z_][A-Za-z0-9_]*)/g, (_, k: string) => {
    used.add(k);
    return encodeURIComponent(String(params[k] ?? ""));
  });
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (!used.has(k) && v != null && k in declared) q.set(k, String(v));
  }
  const qs = q.toString();
  return qs ? `${path}?${qs}` : path;
}

export function matchRoute(route: string, pathname: string): Record<string, string> | null {
  const a = route.split("/").filter(Boolean);
  const b = pathname.split("/").filter(Boolean);
  if (a.length !== b.length) return null;
  const out: Record<string, string> = {};
  for (let i = 0; i < a.length; i++) {
    if (a[i].startsWith(":")) out[a[i].slice(1)] = decodeURIComponent(b[i]);
    else if (a[i] !== b[i]) return null;
  }
  return out;
}

export function withClass(base: string, node: { class?: string }): string {
  return node.class ? `${base} ${node.class}` : base;
}

export type { Env };
