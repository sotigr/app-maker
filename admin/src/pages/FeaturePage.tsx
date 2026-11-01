import { useEffect, useState } from "react";
import { Api, FeatureView, TypeDescription, describeError } from "../api";
import { SchemaForm, Value } from "../form";

export function FeaturePage({ type, id }: { type: string; id: string }) {
  const [f, setF] = useState<FeatureView>();
  const [desc, setDesc] = useState<TypeDescription>();
  const [kids, setKids] = useState<FeatureView[]>([]);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const reload = () => {
    Api.get(type || "_", id).then((x) => {
      setF(x);
      Api.type(x.type).then(setDesc, (e) => setErr(describeError(e)));
    }, (e) => setErr(describeError(e)));
    Api.children(id).then((r) => setKids(r.items), () => setKids([]));
  };
  useEffect(() => {
    setErr("");
    reload();
  }, [type, id]);
  if (!f || !desc) return <p className="error">{err || "Loading…"}</p>;
  // Transition buttons for the current state (spec 13.3).
  const actions: string[] = [];
  for (const [path, sm] of Object.entries(desc.stateMachines || {})) {
    const cur = f.data[path.slice(1)];
    for (const t of sm.transitions) if (t.from === cur) actions.push(t.action);
  }
  type = f.type;
  const run = async (action: string) => {
    setErr("");
    setMsg("");
    let input: any = {};
    if (action === "transfer") {
      const to = prompt("Transfer to principal ID");
      if (!to) return;
      input = { to };
    }
    try {
      const r = await Api.action(type, id, action, input);
      setMsg(`${action}: ok${r.output !== undefined ? ` → ${JSON.stringify(r.output)}` : ""}`);
      reload();
    } catch (e) {
      setErr(describeError(e));
    }
  };
  return (
    <>
      <h1>
        <a href="#/types">Data</a> / <a href={`#/types/${type}`}>{desc.title || type}</a> / <span className="mono">{id.slice(0, 8)}…</span>
      </h1>
      <div className="row">
        <span className="pill">version {f.version}</span>
        {f.parent && <span>parent <a className="mono" href={`#/features/_/${f.parent}`}>{f.parent.slice(0, 8)}…</a></span>}
        {actions.map((a) => <button key={a} onClick={() => run(a)}>{a}</button>)}
        {desc.actions.includes("transfer") && <button onClick={() => run("transfer")}>transfer</button>}
        <button className="danger" onClick={() => confirm("Delete this feature and its children?") && Api.remove(f).then(() => history.back(), (e) => setErr(describeError(e)))}>
          Delete
        </button>
      </div>
      <p className="error">{err}</p>
      <p className="okmsg">{msg}</p>
      <div className="grid2">
        <div className="card">
          <h2>Fields</h2>
          <table>
            <tbody>
              {Object.keys(f.data).sort().map((k) => (
                <tr key={k}><th>{k}</th><td><Value v={f.data[k]} /></td></tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="card">
          <h2>Edit</h2>
          <SchemaForm
            key={f.version}
            schema={desc.views.update}
            initial={f.data}
            submitLabel="Save"
            upload={{ type: f.type, feature: f.id }}
            onSubmit={async (data) => {
              if (Object.keys(data).length === 0) return;
              const n = await Api.update(f, data).catch((e) => Promise.reject(new Error(describeError(e))));
              setF(n);
            }}
          />
        </div>
      </div>
      <div className="card">
        <h2>Children</h2>
        {kids.length === 0 ? <p className="muted">None.</p> : (
          <table>
            <tbody>
              {kids.map((k) => (
                <tr key={k.id}>
                  <td>{k.type}</td>
                  <td><a href={`#/features/${k.type}/${k.id}`}>{String(k.data.title ?? k.data.body ?? k.data.role ?? k.id.slice(0, 8))}</a></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
