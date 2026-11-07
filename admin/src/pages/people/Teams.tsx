// People > Teams and Service accounts: lists, creation, members, roles,
// grants, and API keys (ADR 0027).
import { useEffect, useState } from "react";
import { Api, FeatureView, fieldErrors } from "../../api";
import { Badge, Dialog, EmptyState, Field, Spinner, useConfirm, useToast } from "../../ui/kit";
import { IdChip, RefPicker, RolePicker } from "../../ui/refs";
import { GrantList } from "../../ui/grants";
import { KeysCard, PeopleTabs, ProfileCard, Section, StatusControl, usePrincipal } from "./common";

type Kind = { type: "team" | "serviceAccount"; tab: "teams" | "service-accounts"; noun: string; plural: string; intro: string };
const team: Kind = { type: "team", tab: "teams", noun: "team", plural: "Teams", intro: "Teams hold grants and own records; every member gets them." };
const sa: Kind = { type: "serviceAccount", tab: "service-accounts", noun: "service account", plural: "Service accounts", intro: "Service accounts are non-human principals for integrations; they sign in with API keys only." };

export const Teams = () => <PrincipalList kind={team} />;
export const ServiceAccounts = () => <PrincipalList kind={sa} />;

function PrincipalList({ kind }: { kind: Kind }) {
  const toast = useToast();
  const [q, setQ] = useState("");
  const [items, setItems] = useState<FeatureView[]>();
  const [adding, setAdding] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => {
      const where = q.trim() ? { field: "/displayName", op: "icontains", value: q.trim() } : undefined;
      Api.query({ type: kind.type, where, sort: [{ field: "/displayName", dir: "asc" }], limit: 200 })
        .then((r) => setItems(r.items), (e) => { toast.error(e); setItems([]); });
    }, 250);
    return () => clearTimeout(t);
  }, [q, kind.type]);
  const open = (f: FeatureView) => (location.hash = `#/people/${kind.tab}/${f.id}`);
  return (
    <>
      <PeopleTabs current={kind.tab} />
      <div className="page-head">
        <div><h1>{kind.plural}</h1><p className="muted">{kind.intro}</p></div>
        <button className="primary" onClick={() => setAdding(true)}>Add {kind.noun}</button>
      </div>
      <div className="filters"><input aria-label={`Search ${kind.plural.toLowerCase()}`} placeholder="Search by name" value={q} onChange={(e) => setQ(e.target.value)} /></div>
      {!items ? <Spinner /> : items.length === 0 ? (
        <EmptyState title={q ? "No matches" : `No ${kind.plural.toLowerCase()} yet`}>
          {!q && <button className="link" onClick={() => setAdding(true)}>Add a {kind.noun}</button>}
        </EmptyState>
      ) : (
        <table className="list">
          <thead><tr><th>Name</th>{kind.type === "team" ? <th>Members</th> : <th>Roles</th>}<th>Owner</th><th>Status</th></tr></thead>
          <tbody>
            {items.map((f) => (
              <tr key={f.id} className="clickable" tabIndex={0} onClick={() => open(f)} onKeyDown={(e) => e.key === "Enter" && open(f)}>
                <td><a href={`#/people/${kind.tab}/${f.id}`} onClick={(e) => e.stopPropagation()}>{f.data.displayName || "(no name)"}</a></td>
                {kind.type === "team" ? <td>{(f.data.members || []).length}</td>
                  : <td>{(f.data.roles || []).map((r: string) => <span key={r} className="tag">{r}</span>)}</td>}
                <td onClick={(e) => e.stopPropagation()}><IdChip id={f.data.owner} /></td>
                <td><Badge>{f.data.status || "active"}</Badge></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {adding && <AddPrincipal kind={kind} onClose={() => setAdding(false)} onDone={(id) => { setAdding(false); location.hash = `#/people/${kind.tab}/${id}`; }} />}
    </>
  );
}

function AddPrincipal({ kind, onClose, onDone }: { kind: Kind; onClose: () => void; onDone: (id: string) => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [roles, setRoles] = useState<string[]>([]);
  const [errs, setErrs] = useState<Record<string, string>>({});
  const submit = async () => {
    if (!name.trim()) {
      setErrs({ displayName: "Enter a name." });
      return;
    }
    try {
      const f = await Api.create(kind.type, { displayName: name.trim(), ...(roles.length ? { roles } : {}) });
      toast.ok(`${name.trim()} was added.`);
      onDone(f.id);
    } catch (x) {
      const fe = fieldErrors(x);
      if (Object.keys(fe).length) setErrs(fe);
      else toast.error(x);
    }
  };
  return (
    <Dialog title={`Add ${kind.noun}`} onClose={onClose}
      footer={<><button onClick={onClose}>Cancel</button><button className="primary" onClick={submit}>Add {kind.noun}</button></>}>
      <Field label="Name" required error={errs.displayName}>
        {(c) => <input {...c} autoFocus value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && submit()} />}
      </Field>
      <Field label="App-wide roles" error={errs.roles} help={kind.type === "team" ? "Members get these roles everywhere. Usually you give teams grants on records instead." : undefined}>
        {(c) => <RolePicker control={c} value={roles} onChange={setRoles} />}
      </Field>
    </Dialog>
  );
}

export const TeamPage = ({ id }: { id: string }) => <PrincipalPage kind={team} id={id} />;
export const ServiceAccountPage = ({ id }: { id: string }) => <PrincipalPage kind={sa} id={id} />;

function PrincipalPage({ kind, id }: { kind: Kind; id: string }) {
  const { f, save } = usePrincipal(kind.type, id);
  if (f === undefined) return <Spinner />;
  if (f === null) return <EmptyState title={`${kind.noun} not found`}><a href={`#/people/${kind.tab}`}>Back</a></EmptyState>;
  return (
    <>
      <PeopleTabs current={kind.tab} />
      <nav className="crumbs" aria-label="Breadcrumb"><a href={`#/people/${kind.tab}`}>{kind.plural}</a> / <span>{f.data.displayName}</span></nav>
      <div className="page-head">
        <div><h1>{f.data.displayName || kind.noun}</h1><div className="muted">Owner: <IdChip id={f.data.owner} /></div></div>
        <StatusControl f={f} save={save} noun={kind.noun} />
      </div>
      <div className="grid2">
        <Section title="Profile and roles"><ProfileCard f={f} save={save} /></Section>
        {kind.type === "team" ? <Members f={f} save={save} /> : (
          <Section title="How it signs in"><p className="muted">With API keys only (below). Keys act with this account's roles and grants.</p></Section>
        )}
      </div>
      <Section title="Access to records"><GrantList subject={id} /></Section>
      {kind.type === "serviceAccount" && <KeysCard principal={id} help="Give each integration its own key so you can revoke it alone." />}
    </>
  );
}

function Members({ f, save }: { f: FeatureView; save: (patch: Record<string, unknown>, done: string) => Promise<boolean> }) {
  const toast = useToast();
  const confirm = useConfirm();
  const members: string[] = f.data.members || [];
  const set = async (next: string[], done: string) => {
    try {
      await save({ members: next }, done);
    } catch (e) {
      toast.error(e);
    }
  };
  return (
    <Section title={`Members (${members.length})`}>
      <Field label="Add a member" help="People, service accounts, or other teams.">
        {(c) => <RefPicker control={c} value={null} exclude={[f.id, ...members]} types={["user", "serviceAccount", "team"]} placeholder="Search people, service accounts, teams…"
          onChange={(id) => id && set([...members, id], "Member added.")} />}
      </Field>
      {members.length === 0 ? <EmptyState title="No members yet" /> : (
        <ul className="member-list">
          {members.map((m) => (
            <li key={m}>
              <IdChip id={m} />
              <button className="small-btn danger" onClick={async () => {
                if (await confirm({ title: "Remove this member?", body: <p>They lose the team's grants and ownership at once.</p>, confirm: "Remove", danger: true })) {
                  set(members.filter((x) => x !== m), "Member removed.");
                }
              }}>Remove</button>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
