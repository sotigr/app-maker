// The app: shell, router, login, toasts, and the runtime caches
// (permissions, ref labels, views) shared by every page.
import { useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { OrganonClient, type Feature } from "@organon/client";
import { Node } from "./render";
import {
  buildPath, makeEval, matchRoute, PageContext, RuntimeContext, ScopeContext, useFuncs,
  type EditorHooks, type PageState, type Runtime, type ViewSchema,
} from "./runtime";
import { useSources } from "./sources";
import { describe } from "./steps";
import type { Bundle, PageDTO, ParamSpec, Principal } from "./types";

interface Toast {
  id: number;
  text: string;
  tone: string;
}

export interface RuntimeProviderProps {
  client: OrganonClient;
  bundle: Bundle;
  principal: Principal | null;
  /** Navigation; defaults to the History API. */
  navigate?: (path: string) => void;
  onLogout?: () => void;
  editor?: EditorHooks;
  children: ReactNode;
}

/** RuntimeProvider supplies what nodes need: client, bundle, navigation,
 * batched permission checks, ref labels, and toasts. */
export function RuntimeProvider({ client, bundle, principal, navigate, onLogout, editor, children }: RuntimeProviderProps) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [tick, setTick] = useState(0);
  const perms = useRef(new Map<string, boolean>());
  const pending = useRef(new Map<string, { id: string; op: string }>());
  const labels = useRef(new Map<string, string>());
  const loadingLabels = useRef(new Set<string>());
  const views = useRef(new Map<string, Promise<{ create: ViewSchema; update: ViewSchema }>>());
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const nextToast = useRef(1);

  const flush = useCallback(async () => {
    timer.current = undefined;
    const batch = [...pending.current.entries()];
    pending.current.clear();
    for (let i = 0; i < batch.length; i += 100) {
      const part = batch.slice(i, i + 100);
      try {
        const r = await client.request<{ results: boolean[] }>("POST", "/v1/permissions", { checks: part.map(([, c]) => c) });
        part.forEach(([key], j) => perms.current.set(key, r.results[j] === true));
      } catch {
        part.forEach(([key]) => perms.current.set(key, false));
      }
    }
    setTick((n) => n + 1);
  }, [client]);

  const rt: Runtime = useMemo(() => {
    const go = (path: string) => {
      if (navigate) navigate(path);
      else {
        history.pushState(null, "", path);
        dispatchEvent(new PopStateEvent("popstate"));
      }
    };
    const href = (page: string, params: Record<string, unknown> = {}) => {
      const p = bundle.pages.find((x) => x.name === page);
      return p ? buildPath(p.route, params, p.params || {}) : "#";
    };
    return {
      client, bundle, principal, editor,
      href,
      navigate: (page, params) => go(href(page, params)),
      back: () => history.back(),
      toast: (text, tone = "default") => {
        const id = nextToast.current++;
        setToasts((t) => [...t, { id, text, tone }]);
        setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === "danger" ? 7000 : 4000);
      },
      logout: () => {
        client.auth.logout().finally(() => (onLogout ? onLogout() : location.assign("/")));
      },
      can: (f, op) => {
        const row = f as { id?: string; version?: number } | null;
        if (!row?.id) return false;
        // Answers depend on the feature's state, so its version is part of the key.
        const key = `${row.id}@${row.version ?? 0}|${op}`;
        const v = perms.current.get(key);
        if (v !== undefined) return v;
        if (!pending.current.has(key)) {
          pending.current.set(key, { id: row.id, op });
          if (!timer.current) timer.current = setTimeout(flush, 0);
        }
        return false;
      },
      label: (id) => {
        const l = labels.current.get(id);
        if (l !== undefined) return l;
        const cached = client.cache.get(id);
        if (cached) return labelOf(cached);
        if (!loadingLabels.current.has(id)) {
          loadingLabels.current.add(id);
          client.request<Feature>("GET", `/v1/features/_/${encodeURIComponent(id)}`)
            .then((f) => labels.current.set(id, labelOf(f)), () => labels.current.set(id, id.slice(0, 8)))
            .finally(() => setTick((n) => n + 1));
        }
        return undefined;
      },
      views: (type) => {
        let p = views.current.get(type);
        if (!p) {
          p = client.request<{ views: { create: ViewSchema; update: ViewSchema } }>("GET", `/v1/schema/types/${encodeURIComponent(type)}`)
            .then((d) => ({ create: d.views.create, update: d.views.update }));
          views.current.set(type, p);
        }
        return p;
      },
    };
  }, [client, bundle, principal, editor, navigate, tick]);

  return (
    <RuntimeContext.Provider value={rt}>
      {children}
      <div className="o-toasts" role="status">
        {toasts.map((t) => <div key={t.id} className={`o-toast o-tone-${t.tone}`}>{t.text}</div>)}
      </div>
    </RuntimeContext.Provider>
  );
}

function labelOf(f: Feature): string {
  const d = f.data as Record<string, unknown>;
  const l = d.displayName ?? d.name ?? d.title ?? d.email ?? d.slug;
  return typeof l === "string" && l ? l : f.id.slice(0, 8);
}

function initialState(dto: PageDTO): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(dto.doc.state || {})) {
    out[k] = v.default ?? ({ string: "", integer: 0, number: 0, boolean: false, array: [], object: {} } as Record<string, unknown>)[v.type] ?? null;
  }
  return out;
}

/** PageView renders one page with its params; the runtime comes from RuntimeProvider. */
export function PageView({ dto, params, setTitle = true }: { dto: PageDTO; params: Record<string, unknown>; setTitle?: boolean }) {
  return <PageInner key={dto.name + JSON.stringify(dto.doc.data ?? {})} dto={dto} params={params} setTitle={setTitle} />;
}

function PageInner({ dto, params, setTitle }: { dto: PageDTO; params: Record<string, unknown>; setTitle: boolean }) {
  const rtx = useRuntimeOrThrow();
  const fns = useFuncs();
  const [state, setStateMap] = useState(() => initialState(dto));
  const [dialog, setDialog] = useState<string>();
  const base = useMemo(() => ({ principal: rtx.principal ?? { id: "", kind: "anonymous", roles: [] }, params, state }), [rtx.principal, params, state]);
  const data = useSources(dto.doc.data, base);
  const dataKey = JSON.stringify(Object.values(data).map((v) => (Array.isArray(v) ? v.map((r) => `${r.id}@${r.version}`) : v && (v as { version?: number }).version)));
  const env = useMemo(() => ({ ...base, data }), [base, dataKey, rtx]);
  const page: PageState = useMemo(() => ({
    name: dto.name, state, dialog,
    setState: (k, v) => setStateMap((s) => ({ ...s, [k]: v })),
    openDialog: setDialog,
    closeDialog: () => setDialog(undefined),
  }), [dto.name, state, dialog]);
  const title = makeEval(env, fns).text(dto.doc.title);
  useEffect(() => {
    if (setTitle && title) document.title = rtx.bundle.app.title ? `${title} · ${rtx.bundle.app.title}` : title;
  }, [title, setTitle]);
  return (
    <PageContext.Provider value={page}>
      <ScopeContext.Provider value={env}>
        <div className="o-page" data-page={dto.name}>
          <Node node={dto.doc.body} path="/body" />
        </div>
      </ScopeContext.Provider>
    </PageContext.Provider>
  );
}

function useRuntimeOrThrow(): Runtime {
  const rt = useContext(RuntimeContext);
  if (!rt) throw new Error("@organon/ui: PageView needs a RuntimeProvider");
  return rt;
}

/** resolve finds the page and typed params for a location. */
export function resolve(bundle: Bundle, pathname: string, search: string): { dto: PageDTO; params: Record<string, unknown> } | null {
  for (const dto of bundle.pages) {
    const m = matchRoute(dto.route, pathname);
    if (!m) continue;
    const params: Record<string, unknown> = { ...m };
    const q = new URLSearchParams(search);
    for (const k of Object.keys(dto.params || {})) if (!(k in m) && q.has(k)) params[k] = q.get(k);
    for (const [k, spec] of Object.entries(dto.params || {}) as [string, ParamSpec][]) {
      const v = params[k];
      if (typeof v !== "string") continue;
      if (spec.type === "integer" || spec.type === "number") params[k] = Number(v);
      if (spec.type === "boolean") params[k] = v === "true";
    }
    return { dto, params };
  }
  return null;
}

function Login({ client, title, onLogin }: { client: OrganonClient; title?: string; onLogin: () => void }) {
  const mfa = new URLSearchParams(location.search).get("mfa");
  const [step, setStep] = useState<{ kind: "password" | "verify" | "enroll"; ticket?: string; secret?: string; uri?: string }>(
    { kind: mfa === "verify" ? "verify" : mfa === "enroll" ? "enroll" : "password" });
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [providers, setProviders] = useState<string[]>([]);
  const [err, setErr] = useState("");
  const fail = (e: unknown) => setErr(describe(e));
  useEffect(() => {
    client.auth.providers().then((r) => setProviders(r.providers || []), () => setProviders([]));
  }, [client]);
  useEffect(() => {
    if (step.kind === "enroll" && !step.secret) {
      client.auth.enrollTotp(step.ticket).then((e) => setStep({ kind: "enroll", ticket: e.ticket, secret: e.secret, uri: e.uri }), fail);
    }
  }, [step]);
  const done = () => {
    history.replaceState(null, "", location.pathname);
    onLogin();
  };
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (step.kind === "password") {
      client.auth.login(login, password).then((r) => {
        if (r.mfa) setStep({ kind: r.mfa, ticket: r.ticket });
        else done();
      }, () => setErr("Wrong email or password."));
    } else if (step.kind === "verify") client.auth.verifyMfa(code, step.ticket).then(done, fail);
    else client.auth.confirmTotp(code, step.ticket).then(done, fail);
  };
  return (
    <div className="o-login-wrap">
      <form className="o-card o-login" onSubmit={submit}>
        <h1 className="o-heading">{title || "Sign in"}</h1>
        {step.kind === "password" && (
          <>
            <label className="o-form-field"><span className="o-field-label">Email</span>
              <input className="o-input" autoComplete="username" value={login} onChange={(e) => setLogin(e.target.value)} autoFocus /></label>
            <label className="o-form-field"><span className="o-field-label">Password</span>
              <input className="o-input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} /></label>
          </>
        )}
        {step.kind === "enroll" && (
          <p>Two-factor authentication is required. Add this key to your authenticator app: <code>{step.secret || "…"}</code></p>
        )}
        {step.kind !== "password" && (
          <label className="o-form-field"><span className="o-field-label">Authentication code</span>
            <input className="o-input" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value.trim())} autoFocus /></label>
        )}
        {err && <div className="o-form-error">{err}</div>}
        <button className="o-button o-button-primary" type="submit">{step.kind === "password" ? "Sign in" : "Verify"}</button>
        {step.kind === "password" && providers.map((p) => (
          <button type="button" key={p} className="o-button" onClick={() => client.auth.oidcStart(p, location.pathname)}>Sign in with {p}</button>
        ))}
      </form>
    </div>
  );
}

function usePath(): string {
  const [path, setPath] = useState(location.pathname + location.search);
  useEffect(() => {
    const on = () => setPath(location.pathname + location.search);
    addEventListener("popstate", on);
    return () => removeEventListener("popstate", on);
  }, []);
  return path;
}

function Shell({ bundle, principal, children }: { bundle: Bundle; principal: Principal | null; children: ReactNode }) {
  const rt = useRuntimeOrThrow();
  const app = bundle.app;
  const path = location.pathname;
  return (
    <div className="o-app">
      <header className="o-header">
        <a className="o-brand" href="/" onClick={(e) => { e.preventDefault(); rt.navigate(app.home); }}>
          {app.logo && <img src={`/assets/app/${app.logo.replace(/^assets\//, "")}`} alt="" />}
          <span>{app.title}</span>
        </a>
        <nav className="o-nav">
          {(app.nav || []).map((n, i) => {
            const params: Record<string, unknown> = {};
            const ev = makeEval({ principal }, {});
            for (const [k, v] of Object.entries(n.params || {})) params[k] = ev.ex(v);
            const href = rt.href(n.page, params);
            return (
              <a key={i} href={href} className={href === path ? "o-nav-active" : ""}
                onClick={(e) => { e.preventDefault(); rt.navigate(n.page, params); }}>
                {ev.text(n.label)}
              </a>
            );
          })}
        </nav>
        {principal && <button className="o-button o-button-link" onClick={() => rt.logout()}>Sign out</button>}
      </header>
      <main className="o-main">{children}</main>
    </div>
  );
}

export interface OrganonAppProps {
  client?: OrganonClient;
}

/** OrganonApp is the whole end-user app: it loads the bundle from
 * GET /v1/ui and renders the page matching the location. */
export function OrganonApp({ client: given }: OrganonAppProps) {
  const client = useMemo(() => given || new OrganonClient({ baseUrl: "/" }), [given]);
  const [bundle, setBundle] = useState<Bundle>();
  const [principal, setPrincipal] = useState<Principal | null>();
  const [error, setError] = useState("");
  const path = usePath();
  const load = useCallback(() => {
    Promise.all([
      client.request<Bundle>("GET", "/v1/ui"),
      client.auth.session().then((s) => s.principal, () => null),
    ]).then(([b, p]) => {
      setBundle(b);
      setPrincipal(p);
    }, (e) => setError(describe(e)));
  }, [client]);
  useEffect(load, [load]);
  useEffect(() => {
    if (!bundle?.styles) return;
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = bundle.styles;
    document.head.appendChild(link);
    return () => link.remove();
  }, [bundle?.styles]);
  useEffect(() => {
    if (typeof BroadcastChannel === "undefined") return;
    const ch = new BroadcastChannel("organon-auth");
    ch.onmessage = () => location.assign("/");
    return () => ch.close();
  }, []);
  if (error) return <div className="o-center o-form-error">{error}</div>;
  if (!bundle || principal === undefined) return <div className="o-center"><div className="o-loading" aria-busy="true" /></div>;
  if (bundle.loginRequired || (!principal && !bundle.pages.length)) {
    return <Login client={client} title={bundle.app.title} onLogin={load} />;
  }
  const [pathname, search = ""] = path.split("?");
  const found = pathname === "/" && !bundle.pages.some((p) => p.route === "/")
    ? (() => { const home = bundle.pages.find((p) => p.name === bundle.app.home); return home ? { dto: home, params: {} } : null; })()
    : resolve(bundle, pathname, search);
  return (
    <RuntimeProvider client={client} bundle={bundle} principal={principal} onLogout={load}>
      <Shell bundle={bundle} principal={principal}>
        {found ? <PageView dto={found.dto} params={found.params} />
          : <div className="o-empty">{bundle.app.notFound || "This page does not exist."}</div>}
      </Shell>
    </RuntimeProvider>
  );
}
