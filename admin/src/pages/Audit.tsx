import { useEffect, useState } from "react";
import { Api, AuditRecord, describeError } from "../api";

// Audit log (spec 14.3): who changed what, never the values.
export function Audit({ initial = {} }: { initial?: Record<string, string> }) {
  const [items, setItems] = useState<AuditRecord[]>([]);
  const [next, setNext] = useState<string>();
  const [filter, setFilter] = useState({ action: initial.action || "", actor: initial.actor || "", target: initial.target || "" });
  const [err, setErr] = useState("");
  const load = (before?: string) => {
    const params: Record<string, string> = { limit: "100" };
    for (const [k, v] of Object.entries(filter)) if (v) params[k] = v;
    if (before) params.before = before;
    Api.audit(params).then(
      (r) => {
        setItems((cur) => (before ? [...cur, ...r.items] : r.items));
        setNext(r.next);
      },
      (e) => setErr(describeError(e)),
    );
  };
  useEffect(() => load(), [filter]);
  const field = (k: keyof typeof filter, placeholder: string) => (
    <input style={{ width: 220 }} placeholder={placeholder} value={filter[k]}
      onChange={(e) => setFilter({ ...filter, [k]: e.target.value.trim() })} />
  );
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Audit log</h1>
        {field("action", "action (e.g. updated, login_failed)")}
        {field("actor", "actor ID")}
        {field("target", "feature ID")}
      </div>
      <p className="error">{err}</p>
      <table>
        <thead>
          <tr><th>When</th><th>Action</th><th>Actor</th><th>Target</th><th>Changed</th><th>Detail</th></tr>
        </thead>
        <tbody>
          {items.map((a) => (
            <tr key={a.id}>
              <td>{new Date(a.at).toLocaleString()}</td>
              <td><span className="pill">{a.action}</span></td>
              <td className="mono">{a.actorKind} {a.actor.slice(0, 8)}</td>
              <td className="mono">{a.type ? a.type.split(":").pop() : ""} {a.target?.slice(0, 8)} {a.version ? `v${a.version}` : ""}</td>
              <td className="mono">{(a.changed || []).join(", ")}</td>
              <td>{a.detail}{a.run ? ` run ${a.run.slice(0, 8)}` : ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {next && <button onClick={() => load(next)}>Older</button>}
    </>
  );
}
