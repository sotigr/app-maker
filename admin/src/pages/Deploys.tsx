import { useEffect, useState } from "react";
import { Api, describeError } from "../api";

export function Deploys() {
  const [deploys, setDeploys] = useState<{ hash: string; actor: string; at: string }[]>([]);
  const [err, setErr] = useState("");
  useEffect(() => {
    Api.deploys().then((r) => setDeploys(r.deploys || []), (e) => setErr(describeError(e)));
  }, []);
  return (
    <>
      <h1>Deploys</h1>
      <p className="error">{err}</p>
      <table>
        <thead><tr><th>When</th><th>Manifest</th><th>Actor</th></tr></thead>
        <tbody>
          {deploys.map((d, i) => (
            <tr key={i}>
              <td>{new Date(d.at).toLocaleString()}</td>
              <td className="mono">{d.hash.slice(0, 12)}</td>
              <td className="mono">{d.actor}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="muted">Deploy with <span className="mono">fw plan</span> and <span className="mono">fw apply</span>.</p>
    </>
  );
}
