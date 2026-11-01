// Runtime smoke test of the SDK against a running server:
//   node sdk/ts/smoke/smoke.ts http://127.0.0.1:8080 admin@localhost admin-password-dev
import { createClient } from "./gen/index.ts";
import { FwError } from "../client/src/index.ts";

const [, , base, login, password, transport] = process.argv;
const res = await fetch(base + "/v1/auth/password", { method: "POST", body: JSON.stringify({ login, password, cli: true }) });
const { token } = await res.json();
const live = (transport || "auto") as "auto" | "ws" | "sse";
const a = createClient({ baseUrl: base, token, transport: live });
const b = createClient({ baseUrl: base, token, transport: live });
const assert = (c: unknown, m: string) => { if (!c) { console.error("FAIL", m); process.exit(1); } console.log("ok  ", m); };

const projects = await a.project.query();
assert(projects.items.length >= 1, "seeded projects are readable through the typed client");
const task = await a.task.create({ title: "SDK task", estimate: "1.5" }, projects.items[0].id);
assert(task.data.estimate === "1.5" && task.version === 1, "create returns the projected feature");

// Live updates: client A watches; client B changes the title.
const seen: string[] = [];
const off = a.raw.cache.subscribe(task.id, () => seen.push(String(a.raw.cache.get(task.id)?.data.title)));
const unwatch = a.raw.watch(task.id);
await new Promise((r) => setTimeout(r, 500));
const updated = await b.task.update(task, { title: "Changed by B" });
for (let i = 0; i < 50 && !seen.includes("Changed by B"); i++) await new Promise((r) => setTimeout(r, 100));
assert(seen.includes("Changed by B"), "a change by another client streams into the cache");

// Optimistic update with a stale version rolls back (milestone 7).
let code = "";
try {
  await a.task.update(task, { title: "Stale write" }); // task.version is 1, server has 2
} catch (e) {
  code = (e as FwError).code;
}
assert(code === "version_conflict", "a stale optimistic update rejects with version_conflict");
assert(a.raw.cache.get(task.id)?.data.title === "Changed by B", "the optimistic value was rolled back");

const r = await a.task.actions.start(updated);
assert(r.feature?.data.status === "in_progress", "typed action methods call the server");
unwatch();
off();
a.raw.close();
b.raw.close();
console.log("SDK smoke test passed");
process.exit(0);
