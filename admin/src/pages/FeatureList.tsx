import { useEffect, useState } from "react";
import { Api, FeatureView, TypeDescription, describeError } from "../api";
import { SchemaForm, Value } from "../form";

export function FeatureList({ type }: { type: string }) {
  const [desc, setDesc] = useState<TypeDescription>();
  const [items, setItems] = useState<FeatureView[]>([]);
  const [next, setNext] = useState<string>();
  const [err, setErr] = useState("");
  const [parent, setParent] = useState("");
  const load = (cursor?: string) =>
    Api.query({ type, limit: 50, cursor })
      .then((r) => {
        setItems((old) => (cursor ? [...old, ...r.items] : r.items));
        setNext(r.next);
      })
      .catch((e) => setErr(describeError(e)));
  useEffect(() => {
    setItems([]);
    Api.type(type).then(setDesc, (e) => setErr(describeError(e)));
    load();
  }, [type]);
  const cols = desc ? Object.keys(desc.views.read?.properties || {}).sort().slice(0, 6) : [];
  return (
    <>
      <h1>
        <a href="#/types">Data</a> / {desc?.title || type}
      </h1>
      <p className="error">{err}</p>
      <table>
        <thead>
          <tr>
            <th>ID</th>
            {cols.map((c) => <th key={c}>{c}</th>)}
            <th>v</th>
          </tr>
        </thead>
        <tbody>
          {items.map((f) => (
            <tr key={f.id}>
              <td><a className="mono" href={`#/features/${f.type}/${f.id}`}>{f.id.slice(0, 8)}…</a></td>
              {cols.map((c) => <td key={c}><Value v={f.data[c]} /></td>)}
              <td>{f.version}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {next && <button onClick={() => load(next)}>Load more</button>}
      {desc && (
        <div className="card" style={{ marginTop: 16 }}>
          <h2>New {desc.title || type}</h2>
          <label>Parent (feature ID; empty = root)</label>
          <input value={parent} onChange={(e) => setParent(e.target.value)} placeholder="root" />
          <SchemaForm
            schema={desc.views.create}
            submitLabel="Create"
            upload={{ type, parent: parent || undefined }}
            onSubmit={async (data) => {
              const f = await Api.create(type, data, parent || undefined).catch((e) => Promise.reject(new Error(describeError(e))));
              location.hash = `#/features/${f.type}/${f.id}`;
            }}
          />
        </div>
      )}
    </>
  );
}
