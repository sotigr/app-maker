import { useEffect, useState } from "react";
import { Api, FeatureView, describeError } from "../api";

export function Deploys() {
  const [deploys, setDeploys] = useState<{ hash: string; actor: string; at: string }[]>([]);
  const [err, setErr] = useState("");
  const [migrations, setMigrations] = useState<FeatureView[]>([]);
  useEffect(() => {
    Api.deploys().then((r) => setDeploys(r.deploys || []), (e) => setErr(describeError(e)));
    Api.query({ type: "migration", limit: 100 }).then((r) => setMigrations(r.items), () => setMigrations([]));
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
      <p className="muted">Deploy with <span className="mono">organon plan</span> and <span className="mono">organon apply</span>.</p>
      <h2>Data migrations</h2>
      {migrations.length === 0 ? (
        <p className="muted">No migrations have run.</p>
      ) : (
        <table>
          <thead><tr><th>Type</th><th>To version</th><th>State</th><th>Processed</th><th>Migrated</th><th>Failed</th></tr></thead>
          <tbody>
            {migrations.map((m) => (
              <tr key={m.id}>
                <td className="mono">{m.data.target}</td>
                <td>{m.data.version}</td>
                <td><span className="pill">{m.data.state}</span></td>
                <td>{m.data.processed ?? 0}</td>
                <td>{m.data.migrated ?? 0}</td>
                <td>{m.data.failed ?? 0}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
