// References made readable: labels instead of UUIDs (IdChip), a search
// picker instead of typed IDs (RefPicker), and role choices from app.json
// (RolePicker). Admin usability plan, phase 1.
import { useEffect, useId, useRef, useState } from "react";
import { AdminMeta, Api, FeatureView, TypeDescription } from "../api";
import { CopyButton, FieldControl } from "./kit";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** labelOf picks a human label for a feature. */
export function labelOf(f: FeatureView): string {
  const d = f.data || {};
  for (const k of ["displayName", "title", "name", "label", "email", "number", "slug"]) {
    if (typeof d[k] === "string" && d[k]) return d[k];
  }
  if (f.type === "grant" && d.role) return `${d.role} grant`;
  if (typeof d.body === "string" && d.body) return d.body.length > 40 ? d.body.slice(0, 40) + "…" : d.body;
  return `${f.type} ${f.id.slice(0, 8)}`;
}

/** hrefOf is the console page of a feature: People screens for principals. */
export function hrefOf(type: string, id: string): string {
  switch (type) {
    case "user": return `#/people/users/${id}`;
    case "team": return `#/people/teams/${id}`;
    case "serviceAccount": return `#/people/service-accounts/${id}`;
  }
  return `#/features/${type || "_"}/${id}`;
}

type Resolved = { text: string; type: string; sub?: string; missing?: boolean };
const labels = new Map<string, Promise<Resolved>>();

/** resolve loads a feature's label once per console session. */
export function resolve(id: string): Promise<Resolved> {
  let p = labels.get(id);
  if (!p) {
    p = Api.get("_", id).then(
      (f) => ({ text: labelOf(f), type: f.type, sub: f.type === "user" && f.data.displayName && f.data.email ? f.data.email : undefined }),
      () => ({ text: id.slice(0, 8) + "…", type: "", missing: true }),
    );
    labels.set(id, p);
  }
  return p;
}

/** forget drops a cached label after the feature changed. */
export function forget(id: string) {
  labels.delete(id);
}

/** IdChip shows a feature by its label, links to it, and copies its ID. */
export function IdChip({ id, copy = false }: { id?: string | null; copy?: boolean }) {
  const [r, setR] = useState<Resolved>();
  useEffect(() => {
    if (!id) return;
    let live = true;
    resolve(id).then((x) => live && setR(x));
    return () => { live = false; };
  }, [id]);
  if (!id) return <span className="muted">—</span>;
  if (id === "system" || !UUID.test(id)) return <span className="chip">{id}</span>;
  return (
    <span className="chip" title={id}>
      {r?.missing ? <span className="muted">{r.text}</span>
        : <a href={hrefOf(r?.type || "", id)}>{r ? r.text : "…"}</a>}
      {r?.type && <span className="chip-type">{r.type}</span>}
      {copy && <CopyButton text={id} label="Copy ID" />}
    </span>
  );
}

const descs = new Map<string, Promise<TypeDescription>>();
export function typeDesc(name: string): Promise<TypeDescription> {
  let p = descs.get(name);
  if (!p) {
    p = Api.type(name);
    descs.set(name, p);
    p.catch(() => descs.delete(name));
  }
  return p;
}

/** searchFields are a type's queryable text fields, label-like ones first. */
function searchFields(d: TypeDescription): string[] {
  const props: Record<string, any> = d.views.query?.properties || {};
  const text = Object.keys(props).filter((k) => {
    const s = props[k];
    const t = Array.isArray(s.type) ? s.type[0] : s.type;
    return t === "string" && !s.enum && !s.format?.match(/uuid|date/);
  });
  const rank = (k: string) => { const i = ["displayName", "title", "name", "email", "number", "slug"].indexOf(k); return i < 0 ? 99 : i; };
  return text.sort((a, b) => rank(a) - rank(b)).slice(0, 4);
}

/** search finds features of the given types whose text fields contain q. */
export async function search(types: string[], q: string, limit = 8): Promise<FeatureView[]> {
  q = q.trim();
  if (UUID.test(q)) {
    try {
      const f = await Api.get("_", q);
      return types.length === 0 || types.includes(f.type) ? [f] : [];
    } catch {
      return [];
    }
  }
  const per = await Promise.all(types.map(async (t) => {
    try {
      const fields = searchFields(await typeDesc(t));
      const doc: any = { type: t, limit };
      if (q && fields.length > 0) {
        const preds = fields.map((field) => ({ field: "/" + field, op: "icontains", value: q }));
        doc.where = preds.length === 1 ? preds[0] : { or: preds };
      } else if (q) {
        return [];
      }
      return (await Api.query(doc)).items;
    } catch {
      return [];
    }
  }));
  return per.flat().slice(0, limit * 2);
}

/**
 * RefPicker chooses a feature of the given types by searching their text
 * fields (or pasting an ID). The value is the feature ID.
 */
export function RefPicker({ value, onChange, types, placeholder, control, exclude = [] }: {
  value?: string | null; onChange: (id: string | null) => void; types: string[];
  placeholder?: string; control?: FieldControl; exclude?: string[];
}) {
  const [q, setQ] = useState("");
  const [items, setItems] = useState<FeatureView[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const listId = useId();
  const seq = useRef(0);
  useEffect(() => {
    if (!open) return;
    const n = ++seq.current;
    const t = setTimeout(() => search(types, q).then((r) => {
      if (n === seq.current) {
        setItems(r.filter((f) => !exclude.includes(f.id)));
        setActive(0);
      }
    }), 200);
    return () => clearTimeout(t);
  }, [q, open, types.join(",")]);
  if (value) {
    return (
      <div className="picked">
        <IdChip id={value} />
        <button type="button" className="small-btn" onClick={() => onChange(null)}>Change</button>
      </div>
    );
  }
  const pick = (f: FeatureView) => {
    labels.set(f.id, Promise.resolve({ text: labelOf(f), type: f.type }));
    onChange(f.id);
    setQ("");
    setOpen(false);
  };
  return (
    <div className="picker">
      <input {...control} role="combobox" aria-expanded={open} aria-controls={listId} aria-autocomplete="list"
        aria-activedescendant={open && items[active] ? `${listId}-${active}` : undefined}
        value={q} placeholder={placeholder || "Search by name, or paste an ID…"}
        onFocus={() => setOpen(true)} onBlur={() => setTimeout(() => setOpen(false), 150)}
        onChange={(e) => { setQ(e.target.value); setOpen(true); }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") { e.preventDefault(); setActive((a) => Math.min(a + 1, items.length - 1)); }
          if (e.key === "ArrowUp") { e.preventDefault(); setActive((a) => Math.max(a - 1, 0)); }
          if (e.key === "Enter" && open && items[active]) { e.preventDefault(); pick(items[active]); }
          if (e.key === "Escape" && open) { e.stopPropagation(); setOpen(false); }
        }} />
      {open && (
        <ul id={listId} role="listbox" className="picker-list">
          {items.length === 0 && <li className="muted picker-empty">{q ? "No matches." : "Type to search."}</li>}
          {items.map((f, i) => (
            <li key={f.id} id={`${listId}-${i}`} role="option" aria-selected={i === active}
              className={i === active ? "active" : ""} onMouseDown={(e) => { e.preventDefault(); pick(f); }}>
              <span>{labelOf(f)}</span>
              <span className="muted small">{f.type === "user" && f.data.email && f.data.displayName ? f.data.email : f.type}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

let metaP: Promise<AdminMeta> | undefined;

/** useMeta returns the sign-in setup (roles, providers, password policy). */
export function useMeta(): AdminMeta | undefined {
  const [m, setM] = useState<AdminMeta>();
  useEffect(() => {
    metaP ??= Api.meta();
    metaP.then(setM, () => { metaP = undefined; });
  }, []);
  return m;
}

/** RolePicker toggles app roles; it explains what each role includes. */
export function RolePicker({ value, onChange, control }: { value: string[]; onChange: (roles: string[]) => void; control?: FieldControl }) {
  const meta = useMeta();
  const roles = meta?.roles || [];
  const unknown = value.filter((r) => !roles.some((x) => x.name === r));
  return (
    <div className="role-picker" role="group" aria-describedby={control?.["aria-describedby"]} id={control?.id}>
      {roles.map((r) => {
        const on = value.includes(r.name);
        return (
          <button type="button" key={r.name} aria-pressed={on} className={`toggle${on ? " on" : ""}`}
            title={r.includes.length ? `includes ${r.includes.join(", ")}` : undefined}
            onClick={() => onChange(on ? value.filter((x) => x !== r.name) : [...value, r.name])}>
            {on ? "✓ " : ""}{r.name}
          </button>
        );
      })}
      {unknown.map((r) => (
        <button type="button" key={r} aria-pressed="true" className="toggle on" title="not declared in app.json"
          onClick={() => onChange(value.filter((x) => x !== r))}>✓ {r}?</button>
      ))}
      {meta && roles.length === 0 && <span className="muted">No roles are declared in app.json.</span>}
    </div>
  );
}

/** RoleSelect chooses one role (for grants). */
export function RoleSelect({ value, onChange, control }: { value: string; onChange: (r: string) => void; control?: FieldControl }) {
  const meta = useMeta();
  return (
    <select {...control} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Choose a role…</option>
      {(meta?.roles || []).map((r) => (
        <option key={r.name} value={r.name}>{r.name}{r.includes.length ? ` (includes ${r.includes.join(", ")})` : ""}</option>
      ))}
    </select>
  );
}
