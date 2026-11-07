import { useEffect, useState } from "react";
import { Api, FeatureView, TypeDescription } from "../api";
import { SchemaForm, Value } from "../form";
import { Dialog, EmptyState, Field, Spinner, useConfirm, useToast } from "../ui/kit";
import { IdChip, RefPicker, forget, hrefOf, labelOf } from "../ui/refs";
import { GrantList, principalTypes } from "../ui/grants";

export function FeaturePage({ type, id }: { type: string; id: string }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [f, setF] = useState<FeatureView | null>();
  const [desc, setDesc] = useState<TypeDescription>();
  const [kids, setKids] = useState<FeatureView[]>([]);
  const [busy, setBusy] = useState("");
  const [transfer, setTransfer] = useState(false);
  const [formKey, setFormKey] = useState(0);
  const reload = () => {
    Api.get(type || "_", id).then((x) => {
      setF(x);
      Api.type(x.type).then(setDesc, (e) => toast.error(e));
    }, (e) => { toast.error(e); setF(null); });
    Api.children(id).then((r) => setKids(r.items), () => setKids([]));
  };
  useEffect(() => {
    setF(undefined);
    reload();
  }, [type, id]);
  if (f === null) return <EmptyState title="Record not found"><a href="#/types">Back to Data</a></EmptyState>;
  if (!f || !desc) return <Spinner />;
  // Transition buttons for the current state (spec 13.3).
  const actions: string[] = [];
  for (const [path, sm] of Object.entries(desc.stateMachines || {})) {
    const cur = f.data[path.slice(1)];
    for (const t of sm.transitions) if (t.from === cur) actions.push(t.action);
  }
  type = f.type;
  const run = async (action: string, input: any = {}) => {
    setBusy(action);
    try {
      const r = await Api.action(type, id, action, input);
      toast.ok(`${action}: done${r.output !== undefined ? ` → ${JSON.stringify(r.output)}` : ""}`);
      forget(id);
      reload();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy("");
    }
  };
  const remove = async () => {
    const ok = await confirm({
      title: `Delete “${labelOf(f)}”?`,
      body: <p>The record and everything under it ({kids.length ? `${kids.length}${kids.length === 100 ? "+" : ""} child records` : "no child records"}) are deleted. Deleted records can be restored only from a backup.</p>,
      confirm: "Delete", danger: true,
    });
    if (!ok) return;
    try {
      await Api.remove(f);
      toast.ok("Deleted.");
      location.hash = `#/types/${type}`;
    } catch (e) {
      toast.error(e);
    }
  };
  const conflict = async () => {
    const ok = await confirm({
      title: "This record changed",
      body: <p>Someone saved it after you opened it. Reload to see their version (your unsaved edits are lost), or keep editing and copy your changes first.</p>,
      confirm: "Reload",
    });
    if (ok) {
      reload();
      setFormKey((k) => k + 1);
    }
  };
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <a href="#/types">Data</a> / <a href={`#/types/${type}`}>{desc.title || type}</a> / <span>{labelOf(f)}</span>
      </nav>
      <div className="page-head">
        <div>
          <h1>{labelOf(f)}</h1>
          <div className="row muted">
            <span>{desc.title || type}</span>
            <span>version {f.version}</span>
            {f.parent && <span>in <IdChip id={f.parent} /></span>}
            <IdChip id={f.id} copy />
          </div>
        </div>
        <div className="row wrap">
          {actions.map((a) => <button key={a} disabled={!!busy} onClick={() => run(a)}>{busy === a ? `${a}…` : a}</button>)}
          {desc.actions.includes("transfer") && <button onClick={() => setTransfer(true)}>Transfer ownership</button>}
          <button className="danger" onClick={remove}>Delete</button>
        </div>
      </div>
      <div className="grid2">
        <section className="card">
          <h2>Fields</h2>
          <table>
            <tbody>
              {Object.keys(f.data).sort().map((k) => (
                <tr key={k}><th>{desc.views.read?.properties?.[k]?.title || k}</th><td><Value v={f.data[k]} /></td></tr>
              ))}
            </tbody>
          </table>
        </section>
        <section className="card">
          <h2>Edit</h2>
          <SchemaForm
            key={`${f.version}-${formKey}`}
            schema={desc.views.update}
            initial={f.data}
            submitLabel="Save"
            upload={{ type: f.type, feature: f.id }}
            onConflict={conflict}
            onSubmit={async (data) => {
              if (Object.keys(data).length === 0) {
                toast.info("Nothing changed.");
                return;
              }
              const n = await Api.update(f, data);
              setF(n);
              forget(id);
              toast.ok("Saved.");
            }}
          />
        </section>
      </div>
      <section className="card">
        <h2>Children</h2>
        {kids.length === 0 ? <p className="muted">None.</p> : (
          <table>
            <tbody>
              {kids.map((k) => (
                <tr key={k.id}>
                  <td className="muted">{k.type}</td>
                  <td><a href={hrefOf(k.type, k.id)}>{labelOf(k)}</a></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
      <section className="card">
        <h2>Access</h2>
        <GrantList object={f.id} />
      </section>
      {transfer && <Transfer onClose={() => setTransfer(false)} onPick={(to) => { setTransfer(false); run("transfer", { to }); }} owner={f.data.owner} />}
    </>
  );
}

function Transfer({ owner, onClose, onPick }: { owner?: string; onClose: () => void; onPick: (to: string) => void }) {
  const [to, setTo] = useState<string | null>(null);
  const [err, setErr] = useState("");
  return (
    <Dialog title="Transfer ownership" onClose={onClose}
      footer={<><button onClick={onClose}>Cancel</button><button className="primary" onClick={() => (to ? onPick(to) : setErr("Choose the new owner."))}>Transfer</button></>}>
      {owner && <p>Current owner: <IdChip id={owner} /></p>}
      <Field label="New owner" required error={err}>
        {(c) => <RefPicker control={c} value={to} onChange={setTo} types={principalTypes} exclude={owner ? [owner] : []} />}
      </Field>
    </Dialog>
  );
}
