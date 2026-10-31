import { useEffect, useState } from "react";
import { Api, TypeDescription, describeError } from "../api";

export function Types() {
  const [types, setTypes] = useState<TypeDescription[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    Api.types().then((r) => setTypes(r.types), (e) => setErr(describeError(e)));
  }, []);
  const app = types.filter((t) => !t.id.startsWith("urn:sys:"));
  const sys = types.filter((t) => t.id.startsWith("urn:sys:") && t.name !== "root");
  const table = (list: TypeDescription[]) => (
    <table>
      <thead>
        <tr><th>Type</th><th>ID</th><th>Traits</th><th>Actions</th></tr>
      </thead>
      <tbody>
        {list.map((t) => (
          <tr key={t.id}>
            <td><a href={`#/types/${t.name}`}>{t.title || t.name}</a></td>
            <td className="mono">{t.id}</td>
            <td>{t.traits.map((x) => x.split(":").pop()).join(", ")}</td>
            <td>{t.actions.join(", ")}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
  return (
    <>
      <h1>Data</h1>
      <p className="error">{err}</p>
      <h2>App types</h2>
      {table(app)}
      <h2>System types</h2>
      {table(sys)}
    </>
  );
}
