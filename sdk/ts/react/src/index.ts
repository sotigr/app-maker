// @organon/react: hooks over @organon/client (spec 13.1).
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import type { Feature, OrganonClient, OrganonError, QueryDoc, TypeApi } from "@organon/client";

/** useFeature keeps one feature live; streamed fields update in place. */
export function useFeature<T extends string, D>(api: TypeApi<T, D, any, any>, id: string | undefined, opts: { paths?: string[] } = {}) {
  const client = api.client;
  const [error, setError] = useState<OrganonError>();
  const data = useSyncExternalStore(
    useCallback((cb) => (id ? client.cache.subscribe(id, cb) : () => undefined), [client, id]),
    () => (id ? (client.cache.get(id) as Feature<T, D> | undefined) : undefined),
  );
  useEffect(() => {
    if (!id) return;
    api.get(id).catch(setError);
    return client.watch(id, opts.paths, setError);
  }, [client, id, (opts.paths || []).join(",")]);
  return { data, error };
}

function useLiveList<T extends string, D>(client: OrganonClient, target: Record<string, unknown> | undefined, deps: unknown[]) {
  const [ids, setIds] = useState<string[]>([]);
  const [error, setError] = useState<OrganonError>();
  const [, bump] = useState(0);
  useEffect(() => {
    if (!target) return;
    const off = client.live(target, setIds, setError);
    const unwatch = client.cache.subscribe("*", () => bump((n) => n + 1));
    return () => {
      off();
      unwatch();
    };
  }, deps);
  const data = ids.map((id) => client.cache.get(id)).filter(Boolean) as Feature<T, D>[];
  return { data, error };
}

/** useQuery keeps a live query result: features enter, change, and leave
 * without refetching (spec 13.2). Only eq/in predicates are live. */
export function useQuery<T extends string, D>(api: TypeApi<T, D, any, any>, doc: Omit<QueryDoc, "type"> = {}) {
  const key = JSON.stringify(doc);
  return useLiveList<T, D>(api.client, { target: "query", query: { ...doc, type: api.type } }, [api.client, api.type, key]);
}

/** useChildren keeps a live child list. */
export function useChildren<T extends string, D>(api: TypeApi<T, D, any, any>, parent: string | undefined) {
  return useLiveList<T, D>(api.client, parent ? { target: "children", parent, type: api.type } : undefined, [api.client, api.type, parent]);
}

/** useAction wraps an action call with pending and error state. */
export function useAction<I, O>(fn: (input: I) => Promise<O>) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<OrganonError>();
  const run = useCallback(async (input: I) => {
    setPending(true);
    setError(undefined);
    try {
      return await fn(input);
    } catch (e) {
      setError(e as OrganonError);
      throw e;
    } finally {
      setPending(false);
    }
  }, [fn]);
  return { run, pending, error };
}

/** useSession returns the signed-in principal, or null. */
export function useSession(client: OrganonClient) {
  const [session, setSession] = useState<{ id: string; kind: string; roles: string[] } | null>();
  useEffect(() => {
    client.auth.session().then((s) => setSession(s.principal), () => setSession(null));
    if (typeof BroadcastChannel === "undefined") return;
    const ch = new BroadcastChannel("organon-auth");
    ch.onmessage = () => setSession(null); // logout in another tab
    return () => ch.close();
  }, [client]);
  return session;
}
