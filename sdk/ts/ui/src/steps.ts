// Event handlers: lists of steps run in order (ADR 0016). API steps call
// the public API as the signed-in user; a failing step stops the chain
// and shows the problem unless onError steps are given.
import { OrganonError, type Feature } from "@organon/client";
import { flatten, makeEval, type PageState, type Runtime } from "./runtime";
import type { Env, Funcs } from "./expr";
import type { StepDoc } from "./types";

function errorValue(e: unknown): Record<string, unknown> {
  if (e instanceof OrganonError) return { code: e.code, message: e.message, status: e.status, errors: e.errors };
  return { code: "client", message: e instanceof Error ? e.message : String(e) };
}

/** describe formats an API error for people; t translates the renderer's
 * own messages (server messages are not translated). */
export function describe(e: unknown, t: (text: string) => string = (s) => s): string {
  if (e instanceof OrganonError) {
    const fields = e.errors.map((x) => `${x.path.replace(/^\//, "")}: ${x.message}`).join("; ");
    switch (e.code) {
      case "forbidden":
        return t("You are not allowed to do this.");
      case "version_conflict":
        return t("Someone else changed this; try again.");
      case "invalid_transition":
        return t("That change is not allowed in the current state.");
    }
    return fields || e.message;
  }
  return e instanceof Error ? e.message : String(e);
}

async function load(rt: Runtime, type: string, id: string): Promise<Feature> {
  return rt.client.cache.get(id) || rt.client.get(type, id);
}

/** setPatch turns {"/a": 1, "/b/c": 2} into a merge patch. */
function setPatch(set: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [ptr, v] of Object.entries(set)) {
    const parts = ptr.split("/").slice(1).map((p) => p.replace(/~1/g, "/").replace(/~0/g, "~"));
    let cur = out;
    parts.forEach((p, i) => {
      if (i === parts.length - 1) cur[p] = v;
      else cur = (cur[p] = (cur[p] as Record<string, unknown>) || {}) as Record<string, unknown>;
    });
  }
  return out;
}

/** runSteps runs steps with env; it resolves false when a step failed. */
export async function runSteps(steps: StepDoc[] | undefined, env: Env, rt: Runtime, page: PageState | null, fns: Funcs): Promise<boolean> {
  let result: unknown = env.result ?? null;
  for (const st of steps || []) {
    const ev = makeEval({ ...env, result, error: env.error ?? null }, fns, (s) => rt.t(s, page?.name));
    const map = (v: unknown) => {
      const o: Record<string, unknown> = {};
      for (const [k, e] of Object.entries((v as Record<string, unknown>) || {})) o[k] = ev.ex(e) ?? null;
      return o;
    };
    if (typeof st.confirm === "string" && !globalThis.confirm(ev.text(st.confirm))) return false;
    try {
      const type = st.type as string;
      switch (st.do) {
        case "action": {
          const r = await rt.client.action(type, String(ev.ex(st.id)), st.action as string, map(st.input));
          result = { output: r.output ?? null, feature: flatten(r.feature) };
          break;
        }
        case "create":
          result = flatten(await rt.client.create(type, map(st.data), (ev.ex(st.parent) as string | undefined) || undefined));
          break;
        case "update": {
          const f = await load(rt, type, String(ev.ex(st.id)));
          result = flatten(await rt.client.update(f, setPatch(map(st.set))));
          break;
        }
        case "delete": {
          const f = await load(rt, type, String(ev.ex(st.id)));
          await rt.client.delete(f);
          result = null;
          break;
        }
        case "navigate":
          rt.navigate(st.page as string, map(st.params));
          break;
        case "back":
          rt.back();
          break;
        case "set":
          page?.setState(st.state as string, ev.ex(st.value) ?? null);
          break;
        case "open":
          page?.openDialog(st.dialog as string);
          break;
        case "close":
          page?.closeDialog();
          break;
        case "toast":
          rt.toast(ev.text(st.text), st.tone as string | undefined);
          break;
        case "logout":
          rt.logout();
          break;
      }
    } catch (e) {
      if (st.onError) {
        await runSteps(st.onError, { ...env, result, error: errorValue(e) }, rt, page, fns);
      } else {
        rt.toast(describe(e, rt.t), "danger");
      }
      return false;
    }
  }
  return true;
}
