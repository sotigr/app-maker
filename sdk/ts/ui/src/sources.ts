// Page data sources (ADR 0016): feature, children, query, and search,
// loaded in dependency order and kept live through the SDK where the
// server can stream them.
import { useEffect, useMemo, useState, useSyncExternalStore, useCallback } from "react";
import type { Feature, OrganonClient, QueryDoc } from "@organon/client";
import { makeEval, flatten, useFuncs, useRuntime } from "./runtime";
import type { Env } from "./expr";
import type { Predicate, Row, SourceDoc } from "./types";

const dataRef = /\bdata\.([A-Za-z_][A-Za-z0-9_]*)/g;

function refs(s: SourceDoc): string[] {
  const texts: unknown[] = [s.feature, s.children, s.search];
  const walk = (p?: Predicate) => {
    if (!p) return;
    texts.push(p.value);
    p.and?.forEach(walk);
    p.or?.forEach(walk);
    walk(p.not);
  };
  walk(s.query?.where);
  texts.push(s.query?.scope?.childrenOf, s.query?.scope?.descendantsOf);
  const out: string[] = [];
  for (const t of texts) if (typeof t === "string") for (const m of t.matchAll(dataRef)) out.push(m[1]);
  return out;
}

/** order sorts sources so each comes after the sources it reads. */
export function orderSources(data: Record<string, SourceDoc> = {}): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const visit = (n: string, stack: Set<string>) => {
    if (seen.has(n) || stack.has(n) || !data[n]) return;
    stack.add(n);
    for (const d of refs(data[n])) visit(d, stack);
    seen.add(n);
    out.push(n);
  };
  for (const n of Object.keys(data).sort()) visit(n, new Set());
  return out;
}

/** live reports whether the gateway can stream a query: eq/in predicates
 * combined with and, and no sort (spec 9.4). */
function liveQuery(q: QueryDoc): boolean {
  const ok = (p: Predicate | undefined): boolean =>
    !p || (p.and ? p.and.every(ok) : !p.or && !p.not && (p.op === "eq" || p.op === "in"));
  return !q.sort?.length && ok(q.where as Predicate | undefined);
}

type Ev = ReturnType<typeof makeEval>;

/** A data source's request: undefined while its inputs are not known yet. */
type Req =
  | { kind: "feature"; type: string; id: string }
  | { kind: "list"; key: string; target?: Record<string, unknown>; load: (c: OrganonClient) => Promise<Feature[]> };

function request(s: SourceDoc, ev: Ev): Req | undefined {
  const str = (v: unknown) => {
    const x = ev.ex(v);
    return typeof x === "string" && x !== "" ? x : undefined;
  };
  if (s.feature !== undefined) {
    const id = str(s.feature);
    return id && s.type ? { kind: "feature", type: s.type, id } : undefined;
  }
  if (s.children !== undefined) {
    const parent = str(s.children);
    if (!parent || !s.type) return undefined;
    const type = s.type;
    return {
      kind: "list", key: `children:${parent}:${type}`,
      target: { target: "children", parent, type },
      load: async (c) => (await c.children(parent, type)).items,
    };
  }
  if (s.search !== undefined) {
    const q = str(s.search);
    if (!q || !s.type) return undefined;
    const path = `/v1/search?${new URLSearchParams({ q, type: s.type })}`;
    return {
      kind: "list", key: `search:${path}`,
      load: async (c) => (await c.request<{ items: Feature[] }>("GET", path)).items || [],
    };
  }
  const q = s.query;
  if (!q) return undefined;
  let missing = false;
  const pred = (p?: Predicate): unknown => {
    if (!p) return undefined;
    if (p.and) return { and: p.and.map(pred) };
    if (p.or) return { or: p.or.map(pred) };
    if (p.not) return { not: pred(p.not) };
    const value = ev.ex(p.value);
    if (value === undefined && p.op !== "exists") missing = true;
    return { field: p.field, op: p.op, value };
  };
  const doc: QueryDoc = { type: q.type, limit: q.limit ?? s.limit ?? 100 };
  if (q.where) doc.where = pred(q.where);
  if (q.sort) doc.sort = q.sort;
  if (q.scope) {
    const scope: { childrenOf?: string; descendantsOf?: string } = {};
    if (q.scope.childrenOf !== undefined) scope.childrenOf = str(q.scope.childrenOf);
    if (q.scope.descendantsOf !== undefined) scope.descendantsOf = str(q.scope.descendantsOf);
    if (!scope.childrenOf && !scope.descendantsOf) missing = true;
    doc.scope = scope;
  }
  if (missing) return undefined;
  return {
    kind: "list", key: `query:${JSON.stringify(doc)}`,
    target: liveQuery(doc) ? { target: "query", query: doc } : undefined,
    load: async (c) => (await c.query(doc)).items,
  };
}

function useFeatureSource(client: OrganonClient, req: Req | undefined): Row | null {
  const id = req?.kind === "feature" ? req.id : undefined;
  const type = req?.kind === "feature" ? req.type : undefined;
  const f = useSyncExternalStore(
    useCallback((cb) => (id ? client.cache.subscribe(id, cb) : () => undefined), [client, id]),
    () => (id ? client.cache.get(id) : undefined),
  );
  useEffect(() => {
    if (!id || !type) return;
    client.get(type, id).catch(() => undefined);
    return client.watch(id);
  }, [client, id, type]);
  return useMemo(() => flatten(f), [f]);
}

function useListSource(client: OrganonClient, req: Req | undefined): Row[] | null {
  const list = req?.kind === "list" ? req : undefined;
  const key = list?.key;
  const [ids, setIds] = useState<string[] | null>(null);
  const [, bump] = useState(0);
  useEffect(() => {
    setIds(null);
    if (!list) return;
    let stop = false;
    const offCache = client.cache.subscribe("*", () => bump((n) => n + 1));
    if (list.target) {
      const off = client.live(list.target, (x) => !stop && setIds(x), () => {
        // The gateway refused (for example, access changed): load once.
        list.load(client).then((fs) => !stop && setIds(fs.map((f) => f.id)), () => !stop && setIds([]));
      });
      return () => {
        stop = true;
        off();
        offCache();
      };
    }
    // Not streamable: load now and again shortly after any cached change.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = () => list.load(client).then((fs) => !stop && setIds(fs.map((f) => f.id)), () => !stop && setIds([]));
    load();
    const offReload = client.cache.subscribe("*", () => {
      clearTimeout(timer);
      timer = setTimeout(load, 400);
    });
    return () => {
      stop = true;
      clearTimeout(timer);
      offReload();
      offCache();
    };
  }, [client, key]);
  if (!ids) return null;
  return ids.map((id) => flatten(client.cache.get(id))).filter((r): r is Row => r !== null);
}

/** useSources loads the page's data sources and returns them by name.
 * Sources not loaded yet are null. The hook order is fixed by the page
 * document, which never changes while a page is shown. */
export function useSources(data: Record<string, SourceDoc> | undefined, base: Env): Record<string, unknown> {
  const rt = useRuntime();
  const fns = useFuncs();
  const order = useMemo(() => orderSources(data), [data]);
  const out: Record<string, unknown> = {};
  for (const name of order) {
    const s = data![name];
    const ev = makeEval({ ...base, data: { ...out } }, fns);
    const req = request(s, ev);
    /* eslint-disable react-hooks/rules-of-hooks -- fixed order per page */
    const one = useFeatureSource(rt.client, s.feature !== undefined ? req : undefined);
    const many = useListSource(rt.client, s.feature === undefined ? req : undefined);
    out[name] = s.feature !== undefined ? one : many;
  }
  return out;
}
