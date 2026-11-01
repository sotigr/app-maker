import { Fragment, useEffect, useState } from "react";
import { Api, FeatureView, describeError } from "../api";
import { Value } from "../form";

export function Runs() {
  const [runs, setRuns] = useState<FeatureView[]>([]);
  const [status, setStatus] = useState("");
  const [open, setOpen] = useState<string>();
  const [err, setErr] = useState("");
  const [note, setNote] = useState("");
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const q: any = { type: "flowRun", sort: [{ field: "/startedAt", dir: "desc" }], limit: 100 };
    if (status) q.where = { field: "/status", op: "eq", value: status };
    Api.query(q).then((r) => setRuns(r.items), (e) => setErr(describeError(e)));
  }, [status, tick]);
  const retry = (id: string) => {
    setErr("");
    Api.retryRun(id).then(() => { setNote("Retry queued."); setTick((t) => t + 1); }, (e) => setErr(describeError(e)));
  };
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Flow runs</h1>
        <select style={{ width: 200 }} value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">all</option>
          {["succeeded", "failed", "rejected", "loop_stopped", "dead_letter"].map((s) => <option key={s}>{s}</option>)}
        </select>
      </div>
      <p className="error">{err}</p>
      {note && <p className="muted">{note}</p>}
      <table>
        <thead>
          <tr><th>Started</th><th>Flow</th><th>Trigger</th><th>Status</th><th>Error</th></tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <Fragment key={r.id}>
              <tr onClick={() => setOpen(open === r.id ? undefined : r.id)} style={{ cursor: "pointer" }}>
                <td>{new Date(r.data.startedAt).toLocaleString()}</td>
                <td>{r.data.flow}</td>
                <td>{r.data.trigger}</td>
                <td>
                  <span className="pill">{r.data.status}</span>
                  {r.data.status === "dead_letter" && (
                    <button className="link" onClick={(e) => { e.stopPropagation(); retry(r.id); }}>retry</button>
                  )}
                </td>
                <td><Value v={r.data.error?.message} /></td>
              </tr>
              {open === r.id && (
                <tr><td colSpan={5}><pre className="mono">{JSON.stringify(r.data, null, 2)}</pre></td></tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </>
  );
}
