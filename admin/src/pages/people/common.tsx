// Pieces shared by the People screens: tabs, the API-keys card, and the
// status and profile editors of a principal (ADR 0027).
import { ReactNode, useEffect, useState } from "react";
import { Api, FeatureView, KeyInfo, fieldErrors, isConflict } from "../../api";
import { Badge, Dialog, EmptyState, Field, Secret, Spinner, formatTime, useConfirm, useToast } from "../../ui/kit";
import { RolePicker, forget } from "../../ui/refs";

export function PeopleTabs({ current }: { current: "users" | "teams" | "service-accounts" }) {
  const tab = (id: string, label: string) => (
    <a href={`#/people/${id}`} className={current === id ? "active" : ""} aria-current={current === id ? "page" : undefined}>{label}</a>
  );
  return <nav className="tabs" aria-label="People">{tab("users", "Users")}{tab("teams", "Teams")}{tab("service-accounts", "Service accounts")}</nav>;
}

export function Section({ title, children, actions }: { title: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="card">
      <div className="section-title"><h2>{title}</h2>{actions}</div>
      {children}
    </section>
  );
}

/** usePrincipal loads a user, team, or service account and saves patches. */
export function usePrincipal(type: string, id: string) {
  const toast = useToast();
  const [f, setF] = useState<FeatureView | null>();
  const reload = () => Api.get(type, id).then(setF, (e) => { toast.error(e); setF(null); });
  useEffect(() => { setF(undefined); reload(); }, [type, id]);
  const save = async (patch: Record<string, unknown>, done: string) => {
    if (!f) return false;
    try {
      setF(await Api.update(f, patch));
      forget(id);
      toast.ok(done);
      return true;
    } catch (e) {
      if (isConflict(e)) {
        toast.error(e);
        reload();
      } else {
        throw e;
      }
      return false;
    }
  };
  return { f, setF, reload, save };
}

/** ProfileCard edits the display name and app-wide roles. */
export function ProfileCard({ f, save, extra }: {
  f: FeatureView; save: (patch: Record<string, unknown>, done: string) => Promise<boolean>; extra?: ReactNode;
}) {
  const toast = useToast();
  const [name, setName] = useState(f.data.displayName || "");
  const [roles, setRoles] = useState<string[]>(f.data.roles || []);
  const [errs, setErrs] = useState<Record<string, string>>({});
  useEffect(() => { setName(f.data.displayName || ""); setRoles(f.data.roles || []); }, [f.version]);
  const dirty = name !== (f.data.displayName || "") || JSON.stringify(roles) !== JSON.stringify(f.data.roles || []);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const patch: Record<string, unknown> = {};
    if (name !== (f.data.displayName || "")) patch.displayName = name;
    if (JSON.stringify(roles) !== JSON.stringify(f.data.roles || [])) patch.roles = roles;
    try {
      setErrs({});
      await save(patch, "Saved.");
    } catch (x) {
      const fe = fieldErrors(x);
      if (Object.keys(fe).length) setErrs(fe);
      else toast.error(x);
    }
  };
  return (
    <form onSubmit={submit}>
      <Field label="Name" error={errs.displayName}>{(c) => <input {...c} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
      {extra}
      <Field label="App-wide roles" error={errs.roles} help="Apply everywhere. For one project or record, add a grant below.">
        {(c) => <RolePicker control={c} value={roles} onChange={setRoles} />}
      </Field>
      <div className="row">
        <button className="primary" type="submit" disabled={!dirty}>Save changes</button>
        {dirty && <span className="muted">Unsaved changes</span>}
      </div>
    </form>
  );
}

/** StatusControl suspends or reactivates a principal. */
export function StatusControl({ f, save, noun }: { f: FeatureView; save: (patch: Record<string, unknown>, done: string) => Promise<boolean>; noun: string }) {
  const confirm = useConfirm();
  const toast = useToast();
  const status = f.data.status || "active";
  const toggle = async () => {
    const suspend = status === "active";
    const ok = await confirm(suspend
      ? { title: `Suspend this ${noun}?`, body: <p>They are signed out at once and cannot sign in or use API keys until reactivated. Their data and grants stay.</p>, confirm: "Suspend", danger: true }
      : { title: `Reactivate this ${noun}?`, body: <p>They can sign in again with their existing methods.</p>, confirm: "Reactivate" });
    if (!ok) return;
    try {
      await save({ status: suspend ? "suspended" : "active" }, suspend ? "Suspended." : "Reactivated.");
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <div className="row">
      <Badge>{status}</Badge>
      <button className={status === "active" ? "danger" : ""} onClick={toggle}>{status === "active" ? "Suspend" : "Reactivate"}</button>
    </div>
  );
}

/** KeysCard lists, creates, and revokes a principal's API keys. */
export function KeysCard({ principal, help }: { principal: string; help: string }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [keys, setKeys] = useState<KeyInfo[]>();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [created, setCreated] = useState<string>();
  const load = () => Api.keys(principal).then((r) => setKeys(r.items || []), (e) => { toast.error(e); setKeys([]); });
  useEffect(() => { load(); }, [principal]);
  const create = async () => {
    try {
      const r = await Api.createKey(principal, name.trim() || "API key");
      setCreated(r.key);
      setName("");
      load();
    } catch (e) {
      toast.error(e);
    }
  };
  const revoke = async (k: KeyInfo) => {
    const ok = await confirm({ title: `Revoke “${k.name}”?`, body: <p>Programs using this key stop working immediately. This cannot be undone.</p>, confirm: "Revoke key", danger: true });
    if (!ok) return;
    try {
      await Api.revokeKey(principal, k.prefix);
      toast.ok("Key revoked.");
      load();
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <Section title="API keys" actions={<button onClick={() => setCreating(true)}>Create key</button>}>
      <p className="muted">{help}</p>
      {!keys ? <Spinner /> : keys.length === 0 ? <EmptyState title="No API keys" /> : (
        <table>
          <thead><tr><th>Name</th><th>Starts with</th><th>Created</th><th></th></tr></thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.prefix}>
                <td>{k.name || <span className="muted">(unnamed)</span>}</td>
                <td className="mono">fwk_{k.prefix}_…</td>
                <td>{formatTime(k.createdAt)}</td>
                <td className="right"><button className="small-btn danger" onClick={() => revoke(k)}>Revoke</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {creating && (
        <Dialog title={created ? "Key created" : "Create API key"} onClose={() => { setCreating(false); setCreated(undefined); }}
          footer={created
            ? <button className="primary" onClick={() => { setCreating(false); setCreated(undefined); }}>Done</button>
            : <><button onClick={() => setCreating(false)}>Cancel</button><button className="primary" onClick={create}>Create key</button></>}>
          {created ? <Secret value={created} note="Copy it now: it is shown only once. Use it as a Bearer token." /> : (
            <Field label="Name" help="What uses this key, e.g. “CI deploys”.">
              {(c) => <input {...c} autoFocus value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && create()} />}
            </Field>
          )}
        </Dialog>
      )}
    </Section>
  );
}
