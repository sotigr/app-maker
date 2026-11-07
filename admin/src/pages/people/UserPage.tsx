// People > a user: profile and roles, record access (grants), sign-in and
// security operations, API keys, and recent activity (ADR 0027).
import { useEffect, useState } from "react";
import { Api, AuditRecord, Credentials, Invite, fieldErrors } from "../../api";
import { Badge, CopyButton, Dialog, EmptyState, Field, Spinner, formatTime, useConfirm, useToast } from "../../ui/kit";
import { useMeta } from "../../ui/refs";
import { GrantList } from "../../ui/grants";
import { KeysCard, PeopleTabs, ProfileCard, Section, StatusControl, usePrincipal } from "./common";

export function UserPage({ id, me }: { id: string; me: string }) {
  const { f, save } = usePrincipal("user", id);
  if (f === undefined) return <Spinner />;
  if (f === null) return <EmptyState title="User not found"><a href="#/people/users">Back to users</a></EmptyState>;
  return (
    <>
      <PeopleTabs current="users" />
      <nav className="crumbs" aria-label="Breadcrumb"><a href="#/people/users">Users</a> / <span>{f.data.displayName || f.data.email}</span></nav>
      <div className="page-head">
        <div>
          <h1>{f.data.displayName || f.data.email || "User"}{id === me && <span className="tag">you</span>}</h1>
          <div className="muted">{f.data.email}</div>
        </div>
        <StatusControl f={f} save={save} noun="user" />
      </div>
      <div className="grid2">
        <Section title="Profile and roles">
          <ProfileCard f={f} save={save} extra={
            <Field label="Email" help="Sign-in name. Changing it does not change an existing password login.">
              {(c) => <input {...c} value={f.data.email || ""} readOnly />}
            </Field>} />
        </Section>
        <SecurityCard id={id} self={id === me} name={f.data.displayName || f.data.email} />
      </div>
      <Section title="Access to records"><GrantList subject={id} /></Section>
      <KeysCard principal={id} help="Keys act as this user, for scripts and the organon CLI. Prefer a service account for automation." />
      <Activity actor={id} />
    </>
  );
}

function SecurityCard({ id, self, name }: { id: string; self: boolean; name: string }) {
  const toast = useToast();
  const confirm = useConfirm();
  const meta = useMeta();
  const [c, setC] = useState<Credentials>();
  const [pw, setPw] = useState(false);
  const [invite, setInvite] = useState<Invite>();
  const load = () => Api.credentials(id).then(setC, (e) => toast.error(e));
  useEffect(() => { load(); }, [id]);
  const run = async (o: { title: string; body: string; confirm: string; danger?: boolean }, f: () => Promise<unknown>, done: string) => {
    if (!(await confirm({ ...o, body: <p>{o.body}</p> }))) return;
    try {
      await f();
      toast.ok(done);
      load();
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <Section title="Sign-in and security">
      {!c ? <Spinner /> : (
        <dl className="facts">
          <dt>Password</dt><dd>{c.password ? <>set <span className="muted">({c.login})</span></> : <span className="muted">none</span>}</dd>
          <dt>Single sign-on</dt><dd>{c.oidc.length ? c.oidc.join(", ") : <span className="muted">not linked</span>}</dd>
          <dt>Two-factor (TOTP)</dt><dd>{c.mfa ? <Badge tone="ok">on</Badge> : <span className="muted">off</span>}</dd>
          <dt>API keys</dt><dd>{c.keys}</dd>
        </dl>
      )}
      {c && !c.password && c.oidc.length === 0 && <p className="field-error">This user cannot sign in yet: set a password or send an invite.</p>}
      <div className="row wrap">
        {meta?.passwordEnabled !== false && <button onClick={() => setPw(true)}>{c?.password ? "Set new password" : "Set password"}</button>}
        {(meta?.oidc.length ?? 0) > 0 && <button onClick={() => Api.oidcInvite(id).then(setInvite, (e) => toast.error(e))}>Create sign-in invite</button>}
        {c?.mfa && <button onClick={() => run({ title: "Reset two-factor authentication?", body: `${name} signs in without a code until they enroll again${meta?.mfaAll || (meta?.mfaRoles.length ?? 0) > 0 ? " (they are asked to enroll at their next sign-in if their role requires it)" : ""}. Use this when they lost their device.`, confirm: "Reset", danger: true },
          () => Api.resetMfa(id), "Two-factor authentication reset.")}>Reset two-factor</button>}
        <button onClick={() => run({ title: self ? "Sign out everywhere?" : `Sign ${name} out everywhere?`, body: self ? "Every session ends, including this one." : "Every open session ends; API keys keep working.", confirm: "Sign out", danger: true },
          () => Api.signOutEverywhere(id), "Signed out everywhere.")}>Sign out everywhere</button>
      </div>
      {pw && <SetPassword id={id} onClose={() => setPw(false)} onDone={() => { setPw(false); load(); }} min={meta?.minPasswordLength ?? 12} />}
      {invite && (
        <Dialog title="Sign-in invite" onClose={() => setInvite(undefined)} footer={<button className="primary" onClick={() => setInvite(undefined)}>Done</button>}>
          <p>Send {name} one of these links. Each works once, until {formatTime(invite.expiresAt)}; the first sign-in links that account to this user.</p>
          {Object.entries(invite.links).map(([p, link]) => (
            <div key={p} className="secret"><span className="tag">{p}</span><code>{location.origin + link}</code><CopyButton text={location.origin + link} /></div>
          ))}
        </Dialog>
      )}
    </Section>
  );
}

function SetPassword({ id, min, onClose, onDone }: { id: string; min: number; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [pw, setPw] = useState("");
  const [again, setAgain] = useState("");
  const [errs, setErrs] = useState<Record<string, string>>({});
  const submit = async () => {
    const e: Record<string, string> = {};
    if (pw.length < min) e.password = `Use at least ${min} characters.`;
    else if (pw !== again) e.again = "The passwords differ.";
    setErrs(e);
    if (Object.keys(e).length) return;
    try {
      await Api.setPassword(id, pw);
      toast.ok("Password set. The user was signed out everywhere.");
      onDone();
    } catch (x) {
      const fe = fieldErrors(x);
      if (Object.keys(fe).length) setErrs(fe);
      else toast.error(x);
    }
  };
  return (
    <Dialog title="Set password" onClose={onClose}
      footer={<><button onClick={onClose}>Cancel</button><button className="primary" onClick={submit}>Set password</button></>}>
      <p className="muted">The user is signed out of every session and signs in again with this password.</p>
      <Field label="New password" required error={errs.password || errs.email} help={`At least ${min} characters.`}>
        {(c) => <input {...c} type="password" autoFocus autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} />}
      </Field>
      <Field label="Repeat password" required error={errs.again}>
        {(c) => <input {...c} type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} onKeyDown={(e) => e.key === "Enter" && submit()} />}
      </Field>
    </Dialog>
  );
}

/** Activity lists recent audit records by an actor. */
export function Activity({ actor }: { actor: string }) {
  const [items, setItems] = useState<AuditRecord[]>();
  useEffect(() => { Api.audit({ actor, limit: "10" }).then((r) => setItems(r.items), () => setItems([])); }, [actor]);
  return (
    <Section title="Recent activity">
      {!items ? <Spinner /> : items.length === 0 ? <EmptyState title="No recorded activity" /> : (
        <table>
          <thead><tr><th>When</th><th>Action</th><th>Details</th></tr></thead>
          <tbody>
            {items.map((a) => (
              <tr key={a.id}>
                <td>{formatTime(a.at)}</td>
                <td>{a.action.replace(/_/g, " ")}</td>
                <td className="muted">{[a.type, a.changed?.join(", "), a.detail].filter(Boolean).join(" · ")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <a href={`#/audit?actor=${actor}`}>Full audit log</a>
    </Section>
  );
}
