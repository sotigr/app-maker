// Grants as readable rows ("editor on Launch") with add and remove, for a
// principal (what they can reach) or for a feature (who can reach it,
// including grants inherited from its ancestors).
import { useEffect, useState } from "react";
import { Api, FeatureView, TypeDescription } from "../api";
import { Dialog, EmptyState, Field, Spinner, useConfirm, useToast } from "./kit";
import { IdChip, RefPicker, RoleSelect, forget } from "./refs";

export const principalTypes = ["user", "team", "serviceAccount", "organization"];

let appTypesP: Promise<string[]> | undefined;
function appTypes(): Promise<string[]> {
  appTypesP ??= Api.types().then((r) => r.types.filter((t: TypeDescription) => !t.id.startsWith("urn:sys:")).map((t) => t.name));
  return appTypesP;
}

/** ancestors returns a feature's ancestor IDs, nearest first. */
async function ancestors(id: string): Promise<string[]> {
  const out: string[] = [];
  let cur: string | undefined = id;
  for (let i = 0; i < 12 && cur; i++) {
    const f: FeatureView = await Api.get("_", cur);
    if (i > 0) out.push(f.id);
    cur = f.parent;
  }
  return out;
}

type Row = { grant: FeatureView; inherited?: boolean };

/**
 * GrantList shows the grants of a subject (a principal) or on an object
 * (a feature), and lets admins add and remove them.
 */
export function GrantList({ subject, object }: { subject?: string; object?: string }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [rows, setRows] = useState<Row[]>();
  const [adding, setAdding] = useState(false);
  const load = async () => {
    try {
      if (subject) {
        const r = await Api.query({ type: "grant", where: { field: "/subject", op: "eq", value: subject }, limit: 200 });
        setRows(r.items.map((grant) => ({ grant })));
      } else if (object) {
        const up = await ancestors(object).catch(() => []);
        const direct = await Api.query({ type: "grant", where: { field: "/object", op: "eq", value: object }, limit: 200 });
        const inherited = up.length ? await Api.query({ type: "grant", where: { field: "/object", op: "in", value: up }, limit: 200 }) : { items: [] };
        setRows([...direct.items.map((grant) => ({ grant })), ...inherited.items.map((grant) => ({ grant, inherited: true }))]);
      }
    } catch (e) {
      toast.error(e);
      setRows([]);
    }
  };
  useEffect(() => { setRows(undefined); load(); }, [subject, object]);
  const remove = async (g: FeatureView) => {
    const ok = await confirm({
      title: "Remove this grant?",
      body: <p>The <strong>{g.data.role}</strong> role given to <IdChip id={g.data.subject} /> on <IdChip id={g.data.object} /> is removed at once.</p>,
      confirm: "Remove grant", danger: true,
    });
    if (!ok) return;
    try {
      await Api.remove(g);
      toast.ok("Grant removed.");
      load();
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <>
      <div className="section-head">
        <p className="muted">{subject ? "Roles this principal holds on parts of the data (and everything under them)." : "Who holds a role on this record, directly or through a parent."}</p>
        <button onClick={() => setAdding(true)}>Add grant</button>
      </div>
      {!rows ? <Spinner /> : rows.length === 0 ? <EmptyState title="No grants">{subject ? "App-wide roles still apply." : "Only app-wide roles and owners have access."}</EmptyState> : (
        <table>
          <thead><tr><th>Role</th><th>{subject ? "On" : "Who"}</th><th></th><th></th></tr></thead>
          <tbody>
            {rows.map(({ grant: g, inherited }) => (
              <tr key={g.id}>
                <td><strong>{g.data.role}</strong></td>
                <td><IdChip id={subject ? g.data.object : g.data.subject} /></td>
                <td>{inherited && <span className="muted small">inherited from <IdChip id={g.data.object} /></span>}</td>
                <td className="right"><button className="small-btn danger" onClick={() => remove(g)}>Remove</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && <AddGrant subject={subject} object={object} onClose={() => setAdding(false)} onAdded={() => { setAdding(false); load(); }} />}
    </>
  );
}

function AddGrant({ subject, object, onClose, onAdded }: { subject?: string; object?: string; onClose: () => void; onAdded: () => void }) {
  const toast = useToast();
  const [who, setWho] = useState<string | null>(subject || null);
  const [on, setOn] = useState<string | null>(object || null);
  const [role, setRole] = useState("");
  const [types, setTypes] = useState<string[]>([]);
  const [errs, setErrs] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  useEffect(() => { appTypes().then(setTypes, () => setTypes([])); }, []);
  const submit = async () => {
    const e: Record<string, string> = {};
    if (!who) e.subject = "Choose who gets the role.";
    if (!on) e.object = "Choose the record the role applies to.";
    if (!role) e.role = "Choose a role.";
    setErrs(e);
    if (Object.keys(e).length) return;
    setBusy(true);
    try {
      await Api.create("grant", { subject: who, object: on, role }, on!);
      forget(on!);
      toast.ok(`Granted ${role}.`);
      onAdded();
    } catch (x) {
      toast.error(x);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title="Add grant" onClose={onClose}
      footer={<><button onClick={onClose}>Cancel</button><button className="primary" disabled={busy} onClick={submit}>Add grant</button></>}>
      <p className="muted">A grant gives a role on one record and everything under it.</p>
      {!subject && (
        <Field label="Who" error={errs.subject} required>
          {(c) => <RefPicker control={c} value={who} onChange={setWho} types={principalTypes} placeholder="Search people, teams, service accounts…" />}
        </Field>
      )}
      {!object && (
        <Field label="On" error={errs.object} required help="Search any record by its name or title, or paste its ID.">
          {(c) => <RefPicker control={c} value={on} onChange={setOn} types={types} placeholder="Search records…" />}
        </Field>
      )}
      <Field label="Role" error={errs.role} required>
        {(c) => <RoleSelect control={c} value={role} onChange={setRole} />}
      </Field>
    </Dialog>
  );
}
