// Thin client over the public API. The console uses only the public API
// (spec 13.3): sessions are HttpOnly cookies; state-changing requests send
// the CSRF token from the organon_csrf cookie (double submit, spec 8.3).

export type Problem = {
  code: string;
  status: number;
  detail?: string;
  errors?: { path: string; code: string; message: string }[];
  operation?: number;
  problems?: FlowProblem[] & UiProblem[];
};

export type FlowProblem = { file?: string; flow?: string; pointer: string; message: string };

export class ApiError extends Error {
  constructor(public problem: Problem) {
    super(problem.detail || problem.code);
  }
}

function csrf(): string {
  const m = document.cookie.match(/(?:^|; )organon_csrf=([^;]*)/);
  return m ? decodeURIComponent(m[1]) : "";
}

export async function api<T = any>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
  const h: Record<string, string> = { ...headers };
  if (body !== undefined) h["Content-Type"] = "application/json";
  if (method !== "GET") h["X-CSRF-Token"] = csrf();
  const res = await fetch(path, { method, headers: h, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(data || { code: String(res.status), status: res.status });
  return data as T;
}

export type FeatureView = {
  id: string;
  type: string;
  parent?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  data: Record<string, any>;
};

export type TypeDescription = {
  id: string;
  name: string;
  title?: string;
  version: number;
  traits: string[];
  children: string[];
  views: Record<string, any>;
  actions: string[];
  stateMachines?: Record<string, { initial: string; transitions: { from: string; to: string; action: string }[] }>;
};

export type NodeParam = { name: string; type: string; required?: boolean; map?: boolean; doc?: string };
export type NodeInfo = { name: string; kind: string; description: string; inputs: NodeParam[]; output: string };

export type NodeSpec = {
  node: string;
  in?: Record<string, any>;
  when?: string;
  onError?: string;
  maxItems?: number;
  do?: { nodes: Record<string, NodeSpec>; edges?: [string, string][]; result?: any };
  [k: string]: any;
};

export type FlowDefinition = {
  flow: string;
  version?: number;
  title?: string;
  description?: string;
  trigger: Record<string, any>;
  runAs?: string;
  privileged?: boolean;
  input?: any;
  output?: any;
  timeoutMs?: number;
  trace?: boolean;
  nodes: Record<string, NodeSpec>;
  edges?: [string, string][];
  result?: Record<string, any>;
  "x-layout"?: Record<string, { x: number; y: number }>;
};

export type FlowInfo = { name: string; version: number; trigger: string; on?: string; file: string; definition: FlowDefinition };

export const Api = {
  session: () => api<{ principal: { id: string; kind: string; roles: string[] } }>("GET", "/v1/auth/session"),
  login: (login: string, password: string) =>
    api<{ mfa?: "verify" | "enroll"; ticket?: string }>("POST", "/v1/auth/password", { login, password }),
  providers: () => api<{ providers: string[] }>("GET", "/v1/auth/oidc"),
  audit: (params: Record<string, string>) =>
    api<{ items: AuditRecord[]; next?: string }>("GET", "/v1/audit?" + new URLSearchParams(params).toString()),
  retryRun: (id: string) => api("POST", `/v1/flows/runs/${id}/retry`),
  upload: async (target: { type: string; field: string; feature?: string; parent?: string }, file: File) => {
    const t = await api<{ blob: { id: string; size: number; contentType: string }; uploadUrl: string; method: string; headers: Record<string, string> }>(
      "POST", "/v1/uploads", { ...target, size: file.size, contentType: file.type || "application/octet-stream" });
    const res = await fetch(t.uploadUrl, { method: t.method, headers: t.headers, body: file, credentials: "omit" });
    if (!res.ok) throw new Error(`upload failed: HTTP ${res.status}`);
    return t.blob;
  },
  verifyMfa: (code: string, ticket?: string) => api("POST", "/v1/auth/mfa/verify", { code, ticket }),
  enrollTotp: (ticket?: string) => api<{ secret: string; uri: string; ticket?: string }>("POST", "/v1/auth/mfa/totp/enroll", { ticket }),
  confirmTotp: (code: string, ticket?: string) => api("POST", "/v1/auth/mfa/totp/confirm", { code, ticket }),
  logout: () => api("POST", "/v1/auth/logout"),
  types: () => api<{ types: TypeDescription[] }>("GET", "/v1/schema/types"),
  type: (name: string) => api<TypeDescription>("GET", `/v1/schema/types/${name}`),
  query: (doc: unknown) => api<{ items: FeatureView[]; next?: string }>("POST", "/v1/query", doc),
  get: (type: string, id: string) => api<FeatureView>("GET", `/v1/features/${type}/${id}`),
  children: (id: string, cursor = "") => api<{ items: FeatureView[]; next?: string }>("GET", `/v1/features/${id}/children?limit=100&cursor=${encodeURIComponent(cursor)}`),
  create: (type: string, data: unknown, parent?: string) => api<FeatureView>("POST", `/v1/features/${type}${parent ? `?parent=${parent}` : ""}`, data),
  update: (f: FeatureView, data: unknown) => api<FeatureView>("PATCH", `/v1/features/${f.type}/${f.id}`, data, { "If-Match": `"${f.version}"` }),
  remove: (f: FeatureView) => api("DELETE", `/v1/features/${f.type}/${f.id}`, undefined, { "If-Match": `"${f.version}"` }),
  action: (type: string, id: string, action: string, input: unknown) => api<{ output?: any; feature?: FeatureView }>("POST", `/v1/actions/${type}/${id}/${action}`, input ?? {}),
  flows: () => api<{ flows: FlowInfo[]; nodes: NodeInfo[] }>("GET", "/v1/flows"),
  validateFlow: (definition: FlowDefinition) => api<{ valid: boolean; problems: FlowProblem[] }>("POST", "/v1/flows/validate", { definition }),
  saveFlow: (definition: FlowDefinition) => api<{ saved: string }>("PUT", `/v1/dev/manifest/flows/${definition.flow}`, { definition }),
  deploys: () => api<{ deploys: { hash: string; actor: string; at: string }[] }>("GET", "/v1/deploy"),
  // UI builder (ADR 0016).
  uiSource: () => api<UiSource>("GET", "/v1/ui/source"),
  uiCatalog: () => api<UiCatalog>("GET", "/v1/ui/catalog"),
  validateUi: (files: Record<string, string | null>) => api<{ valid: boolean; problems: UiProblem[] }>("POST", "/v1/ui/validate", { files }),
  savePage: (name: string, document: unknown, from?: string) => api<UiSaved>("PUT", `/v1/dev/manifest/ui/pages/${encodeURIComponent(name)}`, { document, from }),
  deletePage: (name: string) => api<UiSaved>("DELETE", `/v1/dev/manifest/ui/pages/${encodeURIComponent(name)}`),
  saveUiApp: (document: unknown) => api<UiSaved>("PUT", "/v1/dev/manifest/ui/app", { document }),
  saveStyles: (css: string) => api<UiSaved>("PUT", "/v1/dev/manifest/ui/styles", { css }),
  saveAsset: async (path: string, file: File) => {
    const res = await fetch(`/v1/dev/manifest/ui/assets/${path.split("/").map(encodeURIComponent).join("/")}`, {
      method: "PUT", body: file, credentials: "same-origin", headers: { "X-CSRF-Token": csrf(), "Content-Type": file.type || "application/octet-stream" },
    });
    const text = await res.text();
    const data = text ? JSON.parse(text) : undefined;
    if (!res.ok) throw new ApiError(data || { code: String(res.status), status: res.status });
    return data as UiSaved;
  },
};

export type UiProblem = { file: string; pointer?: string; message: string; warning?: boolean };
export type UiSaved = { saved: string[]; removed?: string[]; problems: UiProblem[] };
export type UiSource = {
  dev: boolean;
  app: any | null;
  styles: string | null;
  pages: { name: string; file: string; doc: any }[];
  assets: string[];
  types: Record<string, any>;
};
export type UiProp = { name: string; kind: string; required?: boolean; enum?: string[]; want?: string; doc?: string; item?: UiProp[]; row?: boolean };
export type UiComponent = { name: string; family: string; doc: string; props: UiProp[] | null; events?: string[]; source?: string };
export type UiStep = { name: string; doc: string; props: UiProp[] | null; api?: boolean };
export type UiCatalog = { families: string[]; components: UiComponent[]; steps: UiStep[]; common: UiProp[] };

export function describeError(e: unknown): string {
  if (e instanceof ApiError) {
    const p = e.problem;
    const errs = (p.errors || []).map((x) => `${x.path || "/"}: ${x.message}`).join("; ");
    return `${p.code}${p.detail ? `: ${p.detail}` : ""}${errs ? ` (${errs})` : ""}`;
  }
  return String(e);
}

export type AuditRecord = {
  id: string;
  at: string;
  actor: string;
  actorKind?: string;
  action: string;
  target?: string;
  type?: string;
  version?: number;
  changed?: string[];
  run?: string;
  detail?: string;
};
