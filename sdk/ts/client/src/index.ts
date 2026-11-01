// @fw/client: transport, auth, normalized cache, live subscriptions, and
// optimistic mutations for the framework's public API (spec 13.2). It has
// no UI framework dependency.

/** A feature ID branded with its type name. */
export type FeatureId<T extends string = string> = string & { readonly __type?: T };
/** Decimal values travel as strings to stay exact (spec 3.2). */
export type Decimal = string;
/** RFC 3339 timestamps in UTC. */
export type Timestamp = string;
/** The answer of a login step: a session, or the next MFA step. */
export interface LoginResult {
  principal?: { id: string; kind: string; roles: string[] };
  csrf?: string;
  mfa?: "verify" | "enroll";
  ticket?: string;
}

/** A Blob field value (spec 14.4). Reads add short-lived signed URLs. */
export interface BlobRef {
  id: string;
  size: number;
  contentType: string;
  readonly url?: string;
  readonly thumbnails?: Record<string, string>;
}

/** Where an uploaded blob will be set: an existing feature, or a new one under parent. */
export interface UploadTarget {
  type: string;
  field: string;
  feature?: string;
  parent?: string;
}

/** The envelope every feature is returned in. */
export interface Feature<T extends string = string, D = Record<string, unknown>> {
  readonly id: FeatureId<T>;
  readonly type: T;
  readonly parent?: string;
  readonly version: number;
  readonly createdAt: Timestamp;
  readonly updatedAt: Timestamp;
  data: D;
  /** Set while an optimistic mutation is in flight. */
  readonly pending?: boolean;
}

export interface FieldError {
  path: string;
  code: string;
  message: string;
}

/** Every rejected promise is an FwError carrying the problem code. */
export class FwError extends Error {
  readonly code: string;
  readonly status: number;
  readonly errors: FieldError[];
  readonly current?: Record<string, number>;
  constructor(problem: { code?: string; status?: number; detail?: string; errors?: FieldError[]; current?: Record<string, number> }) {
    super(problem.detail || problem.code || "request failed");
    this.code = problem.code || "internal";
    this.status = problem.status || 0;
    this.errors = problem.errors || [];
    this.current = problem.current;
  }
}

export interface ClientOptions {
  /** Base URL of the server, e.g. "/" in the browser. */
  baseUrl?: string;
  /** Bearer token for service accounts and scripts; browsers use cookies. */
  token?: string;
  /** Custom fetch (tests). */
  fetch?: typeof fetch;
  /** WebSocket constructor (defaults to the global). */
  WebSocket?: typeof WebSocket;
}

type Listener = () => void;

export interface QueryDoc {
  type: string;
  where?: unknown;
  sort?: { field: string; dir?: "asc" | "desc" }[];
  scope?: { childrenOf?: string; descendantsOf?: string };
  select?: string[];
  limit?: number;
  cursor?: string;
}

export interface Page<F> {
  items: F[];
  next?: string;
}

interface Sub {
  msg: Record<string, unknown>;
  onSnapshot: (features: Feature[]) => void;
  onDelta: (m: Delta) => void;
  onLeave: (id: string) => void;
  onError: (e: FwError) => void;
}

interface Delta {
  id: string;
  version: number;
  set: Record<string, unknown>;
  unset: string[];
  enter?: boolean;
  type?: string;
  parent?: string;
}

/** Normalized cache: one entry per feature ID (spec 13.2). */
export class Cache {
  private features = new Map<string, Feature>();
  private listeners = new Map<string, Set<Listener>>();
  get(id: string): Feature | undefined {
    return this.features.get(id);
  }
  put(f: Feature): void {
    this.features.set(f.id, f);
    this.emit(f.id);
  }
  remove(id: string): void {
    this.features.delete(id);
    this.emit(id);
  }
  /** Applies a streamed delta, ignoring stale versions (spec 9.2). */
  applyDelta(d: Delta, fallback?: Partial<Feature>): void {
    const cur = this.features.get(d.id);
    if (cur && !d.enter && d.version !== 0 && d.version <= cur.version && !cur.pending) return;
    const data = { ...(cur?.data || {}) } as Record<string, unknown>;
    for (const [path, v] of Object.entries(d.set)) data[path.slice(1)] = v;
    for (const path of d.unset) delete data[path.slice(1)];
    this.put({
      ...(fallback as Feature),
      ...(cur || {}),
      id: d.id,
      type: (cur?.type || d.type || fallback?.type || "") as string,
      version: d.version || cur?.version || 0,
      data,
      pending: false,
    } as Feature);
  }
  subscribe(id: string, l: Listener): () => void {
    let s = this.listeners.get(id);
    if (!s) this.listeners.set(id, (s = new Set()));
    s.add(l);
    return () => s!.delete(l);
  }
  private emit(id: string) {
    this.listeners.get(id)?.forEach((l) => l());
    this.listeners.get("*")?.forEach((l) => l());
  }
}

function csrfCookie(): string {
  if (typeof document === "undefined") return "";
  const m = document.cookie.match(/(?:^|; )fw_csrf=([^;]*)/);
  return m ? decodeURIComponent(m[1]) : "";
}

/** FwClient is the untyped core used by generated clients. */
export class FwClient {
  readonly cache = new Cache();
  private base: string;
  private fetchFn: typeof fetch;
  private token?: string;
  private WS?: typeof WebSocket;
  private ws?: WebSocket;
  private wsReady?: Promise<WebSocket>;
  private subs = new Map<string, Sub>();
  private nextSid = 1;
  private retry = 0;
  private closed = false;

  constructor(opts: ClientOptions = {}) {
    this.base = (opts.baseUrl || "/").replace(/\/$/, "");
    this.fetchFn = opts.fetch || ((...a) => fetch(...a));
    this.token = opts.token;
    this.WS = opts.WebSocket || (typeof WebSocket !== "undefined" ? WebSocket : undefined);
  }

  /** request performs one API call; failures reject with FwError. */
  async request<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
    const h: Record<string, string> = { ...headers };
    if (body !== undefined) h["Content-Type"] = "application/json";
    if (this.token) h["Authorization"] = `Bearer ${this.token}`;
    else if (method !== "GET") h["X-CSRF-Token"] = csrfCookie();
    const res = await this.fetchFn(this.base + path, {
      method, headers: h, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    const data = text ? JSON.parse(text) : undefined;
    if (!res.ok) throw new FwError({ status: res.status, ...(data || {}) });
    return data as T;
  }

  // Auth (spec 13.2): browser sessions use the HttpOnly cookie only.
  readonly auth = {
    /** login answers a session, or `{mfa: "verify" | "enroll", ticket}` when a TOTP step follows. */
    login: (login: string, password: string) =>
      this.request<LoginResult>("POST", "/v1/auth/password", { login, password }),
    /** providers lists the configured OIDC providers. */
    providers: () => this.request<{ providers: string[] }>("GET", "/v1/auth/oidc"),
    /** oidcStart navigates the browser to the provider; it comes back to `redirect` (a same-origin path),
     * with `?mfa=verify|enroll` when a second factor is needed. */
    oidcStart: (provider: string, redirect = "/", invite?: string) => {
      const q = new URLSearchParams({ redirect });
      if (invite) q.set("invite", invite);
      globalThis.location.assign(`${this.base}/v1/auth/oidc/${encodeURIComponent(provider)}/start?${q}`);
    },
    /** verifyMfa completes a login with a TOTP code (the ticket comes from login, or a cookie after OIDC). */
    verifyMfa: (code: string, ticket?: string) => this.request<LoginResult>("POST", "/v1/auth/mfa/verify", { code, ticket }),
    /** enrollTotp starts TOTP enrollment (signed in, or with an enroll ticket during login). */
    enrollTotp: (ticket?: string) =>
      this.request<{ secret: string; uri: string; ticket?: string }>("POST", "/v1/auth/mfa/totp/enroll", { ticket }),
    /** confirmTotp activates the enrollment; with a ticket it also opens the session. */
    confirmTotp: (code: string, ticket?: string) => this.request<LoginResult | undefined>("POST", "/v1/auth/mfa/totp/confirm", { code, ticket }),
    disableTotp: (code: string) => this.request<void>("POST", "/v1/auth/mfa/totp/disable", { code }),
    logout: async () => {
      await this.request("POST", "/v1/auth/logout");
      if (typeof BroadcastChannel !== "undefined") new BroadcastChannel("fw-auth").postMessage("logout");
    },
    session: () => this.request<{ principal: { id: string; kind: string; roles: string[] } }>("GET", "/v1/auth/session"),
  };

  async get(type: string, id: string): Promise<Feature> {
    const f = await this.request<Feature>("GET", `/v1/features/${type}/${id}`);
    this.cache.put(f);
    return f;
  }

  /** upload sends bytes through a signed URL (spec 14.4) and returns the
   * blob to set on the field in a normal create or update. */
  async upload(target: UploadTarget, body: Blob | ArrayBuffer | Uint8Array, contentType: string): Promise<BlobRef> {
    const size = body instanceof Blob ? body.size : body.byteLength;
    const t = await this.request<{ blob: BlobRef; uploadUrl: string; method: string; headers: Record<string, string> }>(
      "POST", "/v1/uploads", { ...target, size, contentType });
    const res = await this.fetchFn(this.base + t.uploadUrl, { method: t.method, headers: t.headers, body: body as BodyInit, credentials: "omit" });
    if (!res.ok) {
      const text = await res.text();
      throw new FwError({ status: res.status, ...(text ? JSON.parse(text) : {}) });
    }
    return t.blob;
  }

  async create(type: string, data: unknown, parent?: string): Promise<Feature> {
    const f = await this.request<Feature>("POST", `/v1/features/${type}${parent ? `?parent=${encodeURIComponent(parent)}` : ""}`, data);
    this.cache.put(f);
    return f;
  }

  /** update sends a merge patch with If-Match and applies it optimistically;
   * on failure the cached value rolls back and the error is rethrown. */
  async update(f: Feature, patch: Record<string, unknown>): Promise<Feature> {
    const before = this.cache.get(f.id) || f;
    this.cache.put({ ...before, data: { ...before.data, ...patch }, pending: true } as Feature);
    try {
      const n = await this.request<Feature>("PATCH", `/v1/features/${f.type}/${f.id}`, patch, { "If-Match": `"${f.version}"` });
      this.cache.put(n);
      return n;
    } catch (e) {
      this.cache.put(before);
      throw e;
    }
  }

  async delete(f: Feature): Promise<void> {
    await this.request("DELETE", `/v1/features/${f.type}/${f.id}`, undefined, { "If-Match": `"${f.version}"` });
    this.cache.remove(f.id);
  }

  async query(doc: QueryDoc): Promise<Page<Feature>> {
    const p = await this.request<Page<Feature>>("POST", "/v1/query", doc);
    p.items.forEach((f) => this.cache.put(f));
    return p;
  }

  async children(id: string, type?: string, cursor?: string): Promise<Page<Feature>> {
    const q = new URLSearchParams({ limit: "100" });
    if (type) q.set("type", type);
    if (cursor) q.set("cursor", cursor);
    const p = await this.request<Page<Feature>>("GET", `/v1/features/${id}/children?${q}`);
    p.items.forEach((f) => this.cache.put(f));
    return p;
  }

  async action<O = unknown>(type: string, id: string, name: string, input?: unknown): Promise<{ output?: O; feature?: Feature }> {
    const r = await this.request<{ output?: O; feature?: Feature }>("POST", `/v1/actions/${type}/${id}/${name}`, input ?? {});
    if (r.feature) this.cache.put(r.feature);
    return r;
  }

  async ephemeral(type: string, id: string, values: Record<string, unknown>): Promise<void> {
    await this.request("POST", `/v1/ephemeral/${type}/${id}`, values);
  }

  // Live subscriptions over one WebSocket (spec 9.10, 13.2).

  private connect(): Promise<WebSocket> {
    if (this.wsReady) return this.wsReady;
    if (!this.WS) return Promise.reject(new FwError({ code: "unsupported", detail: "no WebSocket available" }));
    const url = this.base.replace(/^http/, "ws") + "/v1/ws";
    this.wsReady = new Promise((resolve, reject) => {
      // Browsers authenticate with the session cookie; scripts pass the
      // bearer token as a subprotocol (headers cannot be set on WebSockets).
      const ws = this.token ? new this.WS!(url, ["fw", `fw.bearer.${this.token}`]) : new this.WS!(url, ["fw"]);
      ws.onopen = () => {
        this.retry = 0;
        this.ws = ws;
        for (const s of this.subs.values()) ws.send(JSON.stringify(s.msg)); // resubscribe: fresh snapshots
        resolve(ws);
      };
      ws.onmessage = (ev: MessageEvent) => this.onMessage(JSON.parse(String(ev.data)));
      ws.onerror = () => reject(new FwError({ code: "transport" }));
      ws.onclose = () => {
        this.ws = undefined;
        this.wsReady = undefined;
        if (this.closed || this.subs.size === 0) return;
        const delay = Math.min(30_000, 500 * 2 ** this.retry++) * (0.5 + Math.random());
        setTimeout(() => this.connect().catch(() => undefined), delay);
      };
    });
    return this.wsReady;
  }

  private onMessage(m: Record<string, any>) {
    if (m.t === "reconnect") {
      this.ws?.close();
      return;
    }
    const s = this.subs.get(m.sid);
    if (!s) return;
    switch (m.t) {
      case "snapshot":
        s.onSnapshot(m.features as Feature[]);
        break;
      case "delta":
        this.cache.applyDelta(m as Delta);
        s.onDelta(m as Delta);
        break;
      case "leave":
        s.onLeave(m.id);
        break;
      case "reset":
        // A reset is followed by a fresh snapshot; nothing to do (spec 9.11).
        break;
      case "error":
        this.subs.delete(m.sid);
        s.onError(new FwError({ code: m.code, detail: m.detail }));
        break;
    }
  }

  /** subscribe opens a live subscription; it returns an unsubscribe function. */
  subscribe(target: Record<string, unknown>, handlers: Omit<Sub, "msg">): () => void {
    const sid = `s${this.nextSid++}`;
    const msg = { t: "subscribe", sid, ...target };
    this.subs.set(sid, { msg, ...handlers });
    if (this.ws && this.ws.readyState === 1) this.ws.send(JSON.stringify(msg));
    else this.connect().catch((e) => handlers.onError(e)); // onopen sends every registered subscription
    return () => {
      this.subs.delete(sid);
      this.ws?.send(JSON.stringify({ t: "unsubscribe", sid }));
    };
  }

  /** watch keeps one feature live in the cache. */
  watch(id: string, paths?: string[], onError?: (e: FwError) => void): () => void {
    return this.subscribe({ target: "feature", id, paths }, {
      onSnapshot: (fs) => fs.forEach((f) => this.cache.put({ ...(this.cache.get(f.id) || ({} as Feature)), ...f })),
      onDelta: () => undefined,
      onLeave: (fid) => this.cache.remove(fid),
      onError: onError || (() => undefined),
    });
  }

  /** live keeps a list (children or query target) live; onChange gets IDs. */
  live(target: Record<string, unknown>, onChange: (ids: string[]) => void, onError?: (e: FwError) => void): () => void {
    let ids: string[] = [];
    return this.subscribe(target, {
      onSnapshot: (fs) => {
        fs.forEach((f) => this.cache.put(f));
        ids = fs.map((f) => f.id);
        onChange(ids);
      },
      onDelta: (d) => {
        if (!ids.includes(d.id)) {
          ids = [...ids, d.id];
          onChange(ids);
        }
      },
      onLeave: (id) => {
        ids = ids.filter((x) => x !== id);
        onChange(ids);
      },
      onError: onError || (() => undefined),
    });
  }

  close(): void {
    this.closed = true;
    this.ws?.close();
  }
}

/** Typed API of one feature type, used by generated clients. */
export class TypeApi<T extends string, D, C = Partial<D>, U = Partial<D>> {
  readonly client: FwClient;
  readonly type: T;
  constructor(client: FwClient, type: T) {
    this.client = client;
    this.type = type;
  }
  get(id: string): Promise<Feature<T, D>> {
    return this.client.get(this.type, id) as Promise<Feature<T, D>>;
  }
  create(data: C, parent?: string): Promise<Feature<T, D>> {
    return this.client.create(this.type, data, parent) as Promise<Feature<T, D>>;
  }
  update(f: Feature<T, D>, patch: U): Promise<Feature<T, D>> {
    return this.client.update(f as Feature, patch as Record<string, unknown>) as Promise<Feature<T, D>>;
  }
  delete(f: Feature<T, D>): Promise<void> {
    return this.client.delete(f as Feature);
  }
  query(doc: Omit<QueryDoc, "type"> = {}): Promise<Page<Feature<T, D>>> {
    return this.client.query({ ...doc, type: this.type }) as Promise<Page<Feature<T, D>>>;
  }
  cached(id: string): Feature<T, D> | undefined {
    return this.client.cache.get(id) as Feature<T, D> | undefined;
  }
}
