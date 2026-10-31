import { useEffect, useState } from "react";
import { Api, FlowInfo, describeError } from "../api";

export function Flows() {
  const [flows, setFlows] = useState<FlowInfo[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    Api.flows().then((r) => setFlows(r.flows), (e) => setErr(describeError(e)));
  }, []);
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Flows</h1>
        <a href="#/flows/new"><button className="primary">New flow</button></a>
      </div>
      <p className="error">{err}</p>
      <table>
        <thead>
          <tr><th>Flow</th><th>Trigger</th><th>On</th><th>File</th><th>Nodes</th></tr>
        </thead>
        <tbody>
          {flows.map((f) => (
            <tr key={f.name}>
              <td><a href={`#/flows/${encodeURIComponent(f.name)}`}>{f.definition.title || f.name}</a> <span className="muted">{f.name} v{f.version}</span></td>
              <td>{f.trigger}</td>
              <td className="mono">{f.on?.split(":").pop()}</td>
              <td className="mono">{f.file}</td>
              <td>{Object.keys(f.definition.nodes || {}).length}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="muted">Saving from the editor writes the flow file back into the manifest; it works under <span className="mono">fw dev</span>. Elsewhere use fw plan / fw apply.</p>
    </>
  );
}
