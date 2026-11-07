// People > Users: list, search, and add users the way sign-in needs them
// (POST /v1/auth/users with a password or an OIDC invite; ADR 0027).
import { useEffect, useState } from "react";
import { Api, Credentials, FeatureView, Invite, fieldErrors } from "../../api";
import { Badge, CopyButton, Dialog, EmptyState, Field, Secret, Spinner, useToast } from "../../ui/kit";
import { useMeta, RolePicker } from "../../ui/refs";
import { PeopleTabs } from "./common";

export function Users() {
  const toast = useToast();
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("active");
  const [role, setRole] = useState("");
  const [items, setItems] = useState<FeatureView[]>();
  const [next, setNext] = useState<string>();
  const [adding, setAdding] = useState(false);
  const meta = useMeta();
  const load = async (cursor?: string) => {
    const preds: any[] = [];
    if (q.trim()) preds.push({ or: [{ field: "/displayName", op: "icontains", value: q.trim() }, { field: "/email", op: "icontains", value: q.trim() }] });
    if (status) preds.push({ field: "/status", op: "eq", value: status });
    try {
      const r = await Api.query({ type: "user", where: preds.length ? { and: preds } : undefined, sort: [{ field: "/displayName", dir: "asc" }], limit: 50, cursor });
      setItems((old) => (cursor ? [...(old || []), ...r.items] : r.items));
      setNext(r.next);
    } catch (e) {
      toast.error(e);
      setItems([]);
    }
  };
  useEffect(() => {
    const t = setTimeout(() => load(), 250);
    return () => clearTimeout(t);
  }, [q, status]);
  const shown = (items || []).filter((u) => !role || (u.data.roles || []).includes(role));
  return (
    <>
      <PeopleTabs current="users" />
      <div className="page-head">
        <h1>Users</h1>
        <button className="primary" onClick={() => setAdding(true)}>Add user</button>
      </div>
      <div className="filters">
        <input aria-label="Search users" placeholder="Search by name or email" value={q} onChange={(e) => setQ(e.target.value)} />
        <select aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="active">Active</option>
          <option value="suspended">Suspended</option>
          <option value="">All statuses</option>
        </select>
        <select aria-label="Role" value={role} onChange={(e) => setRole(e.target.value)}>
          <option value="">Any role</option>
          {(meta?.roles || []).map((r) => <option key={r.name}>{r.name}</option>)}
        </select>
      </div>
      {!items ? <Spinner /> : shown.length === 0 ? (
        <EmptyState title={q || role || status !== "active" ? "No users match" : "No users yet"}>
          {q || role ? "Try another search or filter." : <button className="link" onClick={() => setAdding(true)}>Add the first user</button>}
        </EmptyState>
      ) : (
        <table className="list">
          <thead><tr><th>Name</th><th>Email</th><th>Roles</th><th>Status</th><th>Sign-in</th></tr></thead>
          <tbody>
            {shown.map((u) => (
              <tr key={u.id} className="clickable" tabIndex={0} onClick={() => (location.hash = `#/people/users/${u.id}`)}
                onKeyDown={(e) => e.key === "Enter" && (location.hash = `#/people/users/${u.id}`)}>
                <td><a href={`#/people/users/${u.id}`} onClick={(e) => e.stopPropagation()}>{u.data.displayName || <span className="muted">(no name)</span>}</a></td>
                <td>{u.data.email || <span className="muted">—</span>}</td>
                <td>{(u.data.roles || []).map((r: string) => <span key={r} className="tag">{r}</span>)}</td>
                <td><Badge>{u.data.status || "active"}</Badge></td>
                <td><SignIn id={u.id} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {next && <button onClick={() => load(next)}>Load more</button>}
      {adding && <AddUser onClose={() => setAdding(false)} onDone={(id) => { setAdding(false); location.hash = `#/people/users/${id}`; }} />}
    </>
  );
}

/** SignIn summarizes a user's sign-in methods in a list row. */
export function SignIn({ id }: { id: string }) {
  const [c, setC] = useState<Credentials | null>();
  useEffect(() => { Api.credentials(id).then(setC, () => setC(null)); }, [id]);
  if (c === undefined) return <span className="muted">…</span>;
  if (c === null) return <span className="muted">—</span>;
  const parts = [c.password ? "password" : "", ...c.oidc].filter(Boolean);
  return (
    <span>
      {parts.length ? parts.join(", ") : <Badge tone="warn">cannot sign in</Badge>}
      {c.mfa && <span className="tag">MFA</span>}
    </span>
  );
}

function generatePassword(n: number): string {
  const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const buf = new Uint32Array(Math.max(n, 16));
  crypto.getRandomValues(buf);
  return [...buf].map((x) => chars[x % chars.length]).join("");
}

/** AddUser creates a user who can actually sign in. */
function AddUser({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const toast = useToast();
  const meta = useMeta();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [roles, setRoles] = useState<string[]>([]);
  const [method, setMethod] = useState<"password" | "invite">("password");
  const [password, setPassword] = useState("");
  const [shown, setShown] = useState(false);
  const [generated, setGenerated] = useState(false);
  const [errs, setErrs] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ id: string; password?: string; invite?: Invite }>();
  const min = meta?.minPasswordLength ?? 12;
  const oidc = meta?.oidc || [];
  useEffect(() => { if (!meta?.passwordEnabled && oidc.length) setMethod("invite"); }, [meta]);

  const submit = async () => {
    const e: Record<string, string> = {};
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) e.email = "Enter a valid email address; the user signs in with it.";
    if (!name.trim()) e.displayName = "Enter the name shown in the app.";
    if (method === "password" && password.length < min) e.password = `Use at least ${min} characters.`;
    setErrs(e);
    if (Object.keys(e).length) return;
    setBusy(true);
    try {
      const { id } = await Api.createUser({ email: email.trim(), displayName: name.trim(), roles,
        ...(method === "password" ? { password } : { invite: true }) });
      if (method === "invite") {
        const invite = await Api.oidcInvite(id);
        setResult({ id, invite });
      } else {
        setResult({ id, password: generated ? password : undefined });
      }
      toast.ok(`${name.trim()} was added.`);
    } catch (x) {
      const fe = fieldErrors(x);
      if (Object.keys(fe).length) setErrs(fe);
      else toast.error(x);
    } finally {
      setBusy(false);
    }
  };

  if (result) {
    return (
      <Dialog title="User added" onClose={() => onDone(result.id)}
        footer={<button className="primary" autoFocus onClick={() => onDone(result.id)}>Open user</button>}>
        {result.invite ? (
          <>
            <p>Send {name} one of these sign-in links. Each works once, until {new Date(result.invite.expiresAt).toLocaleString()}.</p>
            {Object.entries(result.invite.links).map(([p, link]) => (
              <div key={p} className="secret"><span className="tag">{p}</span><code>{location.origin + link}</code><CopyButton text={location.origin + link} /></div>
            ))}
          </>
        ) : (
          <>
            <p>{name} can sign in with <strong>{email}</strong> and the password you chose.</p>
            {result.password && <Secret value={result.password} note="Share it privately; it is not shown again." />}
          </>
        )}
      </Dialog>
    );
  }
  return (
    <Dialog title="Add user" onClose={onClose}
      footer={<><button onClick={onClose}>Cancel</button><button className="primary" disabled={busy} onClick={submit}>{busy ? "Adding…" : "Add user"}</button></>}>
      <Field label="Email" required error={errs.email} help="Used to sign in.">
        {(c) => <input {...c} type="email" autoFocus autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />}
      </Field>
      <Field label="Name" required error={errs.displayName}>
        {(c) => <input {...c} value={name} onChange={(e) => setName(e.target.value)} />}
      </Field>
      <Field label="Roles" error={errs.roles} help="App-wide roles. Roles on single records are given as grants on the user's page.">
        {(c) => <RolePicker control={c} value={roles} onChange={setRoles} />}
      </Field>
      <fieldset className="choice">
        <legend>How will they sign in?</legend>
        {meta?.passwordEnabled !== false && (
          <label className="radio"><input type="radio" name="method" checked={method === "password"} onChange={() => setMethod("password")} /> With a password you set now</label>
        )}
        {oidc.length > 0 && (
          <label className="radio"><input type="radio" name="method" checked={method === "invite"} onChange={() => setMethod("invite")} /> With {oidc.join(" or ")} (you get an invite link to send)</label>
        )}
      </fieldset>
      {method === "password" && (
        <Field label="Password" required error={errs.password} help={`At least ${min} characters.`}>
          {(c) => (
            <div className="input-row">
              <input {...c} type={shown ? "text" : "password"} autoComplete="new-password" value={password} onChange={(e) => { setPassword(e.target.value); setGenerated(false); }} />
              <button type="button" className="small-btn" onClick={() => setShown((s) => !s)}>{shown ? "Hide" : "Show"}</button>
              <button type="button" className="small-btn" onClick={() => { setPassword(generatePassword(Math.max(min, 16))); setGenerated(true); setShown(true); }}>Generate</button>
            </div>
          )}
        </Field>
      )}
    </Dialog>
  );
}
