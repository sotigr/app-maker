// Forms and commands: form (create, update, action input), button, menu.
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { OrganonError, type BlobRef } from "@organon/client";
import { register, type ComponentProps } from "../render";
import { enumLabel, flatten, typeMeta, useEval, useFuncs, usePage, useRuntime, withClass, type ViewSchema } from "../runtime";
import { describe, runSteps } from "../steps";
import { labelOf, RefLabel } from "./data";
import type { FieldMeta, JsonSchema, Row, StepDoc } from "../types";

interface FieldSpec {
  path: string;
  label?: string;
  widget?: string;
  help?: string;
  labelField?: string;
}

interface Field {
  name: string;
  label: string;
  help?: string;
  widget: string;
  required: boolean;
  enum?: string[];
  meta?: FieldMeta;
  labelField?: string;
}

function widgetFor(meta: FieldMeta | undefined, schema: { type?: string | string[]; enum?: unknown[]; format?: string; maxLength?: number } | undefined): string {
  const kind = meta?.kind;
  if (kind === "ref") return "ref";
  if (kind === "blob") return "file";
  if (kind === "decimal") return "decimal";
  if (kind === "enum" || schema?.enum) return "select";
  if (kind === "bool") return "checkbox";
  if (kind === "time" || schema?.format === "date-time") return "datetime";
  if (kind === "int64" || kind === "float") return "number";
  const t = Array.isArray(schema?.type) ? schema?.type[0] : schema?.type;
  if (t === "boolean") return "checkbox";
  if (t === "integer" || t === "number") return "number";
  if (schema?.maxLength && schema.maxLength > 500) return "textarea";
  return "text";
}

function toInput(v: unknown, widget: string): string {
  if (v == null) return "";
  if (widget === "datetime") {
    const d = new Date(String(v));
    if (isNaN(d.getTime())) return "";
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  return typeof v === "string" || typeof v === "number" ? String(v) : "";
}

function fromInput(s: string, widget: string, meta?: FieldMeta): unknown {
  if (s === "") return null;
  if (widget === "datetime") return new Date(s).toISOString();
  if (widget === "number") return meta?.kind === "int64" && meta.format !== undefined ? s : Number(s);
  return s;
}

function RefPicker({ value, onChange, targets, labelField }: { value: string | null; onChange(v: string | null): void; targets: string[]; labelField?: string }) {
  const rt = useRuntime();
  const [options, setOptions] = useState<Row[]>([]);
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  useEffect(() => {
    if (!open || options.length) return;
    Promise.all(targets.map((t) => rt.client.query({ type: t, limit: 100 }).then((p) => p.items, () => [])))
      .then((all) => setOptions(all.flat().map((f) => flatten(f)!)));
  }, [open]);
  const label = (r: Row) => String(r[labelField || ""] ?? r.displayName ?? r.name ?? r.title ?? r.email ?? r.id.slice(0, 8));
  const shown = options.filter((r) => !q || label(r).toLowerCase().includes(q.toLowerCase())).slice(0, 30);
  return (
    <div className="o-ref-picker">
      <button type="button" className="o-input o-ref-value" onClick={() => setOpen(!open)}>
        {value ? <RefLabel id={value} /> : <span className="o-muted">{rt.t("Choose…")}</span>}
      </button>
      {open && (
        <div className="o-ref-menu">
          <input className="o-input" autoFocus placeholder={rt.t("Search")} value={q} onChange={(e) => setQ(e.target.value)} />
          <button type="button" className="o-ref-option o-muted" onClick={() => { onChange(null); setOpen(false); }}>{rt.t("None")}</button>
          {shown.map((r) => (
            <button type="button" key={r.id} className="o-ref-option" onClick={() => { onChange(r.id); setOpen(false); }}>{label(r)}</button>
          ))}
        </div>
      )}
    </div>
  );
}

register("form", ({ node }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const fns = useFuncs();
  const page = usePage();
  const type = node.type as string;
  const mode = node.mode as "create" | "update" | "action";
  const meta = typeMeta(rt, type);
  const [view, setView] = useState<ViewSchema | null>(null);
  const id = mode === "create" ? undefined : (ev.ex(node.id) as string | undefined);
  const feature = useSyncExternalStore(
    useCallback((cb: () => void) => (id ? rt.client.cache.subscribe(id, cb) : () => undefined), [rt.client, id]),
    () => (id ? rt.client.cache.get(id) : undefined),
  );
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");
  const [busy, setBusy] = useState(false);
  const dirty = useRef(false);

  useEffect(() => {
    if (mode === "action") {
      setView((meta?.actions[node.action as string] as ViewSchema | null) || { properties: {} });
      return;
    }
    rt.views(type).then((v) => setView(mode === "create" ? v.create : v.update), () => setView({ properties: {} }));
  }, [type, mode]);
  useEffect(() => {
    if (id && !feature) rt.client.get(type, id).catch(() => undefined);
  }, [id]);
  // Update forms start from the current values and follow live changes
  // until the user edits; then the server reports conflicts on save.
  useEffect(() => {
    if (mode === "update" && feature && !dirty.current) setValues({ ...(feature.data as Record<string, unknown>) });
  }, [feature?.version]);

  const fields: Field[] = useMemo(() => {
    if (!view) return [];
    const props = view.properties || {};
    const specs: FieldSpec[] = (node.fields as FieldSpec[] | undefined) ||
      Object.keys(props).filter((k) => !meta?.fields[`/${k}`]?.readOnly).map((k) => ({ path: `/${k}` }));
    return specs.filter((s) => s.path.slice(1) in props).map((s) => {
      const name = s.path.slice(1);
      const schema = props[name];
      const fm = mode === "action" ? undefined : meta?.fields[s.path];
      const widget = s.widget && s.widget !== "auto" ? s.widget : widgetFor(fm, schema);
      const en = (fm?.enum || (schema?.enum as string[] | undefined))?.filter((x) => x != null);
      return {
        name, widget, enum: en, meta: fm, labelField: s.labelField,
        label: s.label !== undefined ? ev.text(s.label) : schema?.title || labelOf(s.path, fm),
        help: s.help !== undefined ? ev.text(s.help) : undefined,
        required: (view.required || []).includes(name),
      };
    });
  }, [view, node.fields, ev]);

  const set = (name: string, v: unknown) => {
    dirty.current = true;
    setValues((x) => ({ ...x, [name]: v }));
    setErrors((x) => ({ ...x, [name]: "" }));
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErrors({});
    setFormError("");
    const data: Record<string, unknown> = {};
    for (const f of fields) {
      const v = values[f.name];
      if (mode === "update") {
        const before = (feature?.data as Record<string, unknown> | undefined)?.[f.name];
        if (JSON.stringify(before ?? null) !== JSON.stringify(v ?? null)) data[f.name] = v ?? null;
      } else if (v != null && v !== "") data[f.name] = v;
    }
    try {
      let result: unknown;
      if (mode === "create") {
        result = flatten(await rt.client.create(type, data, (ev.ex(node.parent) as string | undefined) || undefined));
        setValues({});
      } else if (mode === "update") {
        if (!feature) throw new Error("not loaded yet");
        result = Object.keys(data).length ? flatten(await rt.client.update(feature, data)) : flatten(feature);
        dirty.current = false;
      } else {
        const r = await rt.client.action(type, id!, node.action as string, data);
        result = { output: r.output ?? null, feature: flatten(r.feature) };
        setValues({});
      }
      await runSteps((node.on as Record<string, StepDoc[]> | undefined)?.submit, { ...ev.env, result, form: { values: data } }, rt, page, fns);
    } catch (err) {
      if (err instanceof OrganonError && err.errors.length) {
        const byField: Record<string, string> = {};
        for (const x of err.errors) byField[x.path.replace(/^\//, "").split("/")[0]] = x.message;
        setErrors(byField);
        const unmatched = err.errors.filter((x) => !fields.some((f) => f.name === x.path.replace(/^\//, "").split("/")[0]));
        if (unmatched.length) setFormError(unmatched.map((x) => `${x.path}: ${x.message}`).join("; "));
      } else setFormError(describe(err, rt.t));
    } finally {
      setBusy(false);
    }
  };

  const className = withClass("o-form", node);
  if (!view) return <div className="o-loading" aria-busy="true" />;
  if (mode === "update" && !feature) return <div className="o-loading" aria-busy="true" />;
  return (
    <form className={className} onSubmit={submit} noValidate>
      {fields.map((f) => (
        <label key={f.name} className={`o-form-field${errors[f.name] ? " o-invalid" : ""}`}>
          <span className="o-field-label">{f.label}{f.required && <span className="o-required"> *</span>}</span>
          <Input field={f} value={values[f.name]} set={(v) => set(f.name, v)} type={type} id={id} parent={ev.ex(node.parent) as string | undefined} />
          {f.help && <span className="o-help">{f.help}</span>}
          {errors[f.name] && <span className="o-field-error">{errors[f.name]}</span>}
        </label>
      ))}
      {formError && <div className="o-form-error">{formError}</div>}
      <div className="o-row o-gap-sm">
        <button className="o-button o-button-primary" type="submit" disabled={busy}>{ev.text(node.submit) || rt.t("Save")}</button>
      </div>
    </form>
  );
});

function Input({ field, value, set, type, id, parent }: { field: Field; value: unknown; set(v: unknown): void; type: string; id?: string; parent?: string }) {
  const rt = useRuntime();
  const [uploading, setUploading] = useState(false);
  switch (field.widget) {
    case "checkbox":
      return <input type="checkbox" checked={value === true} onChange={(e) => set(e.target.checked)} />;
    case "select":
      return (
        <select className="o-input" value={value == null ? "" : String(value)} onChange={(e) => set(e.target.value === "" ? null : e.target.value)}>
          <option value="">—</option>
          {(field.enum || []).map((o) => <option key={o} value={o}>{enumLabel(field.meta, o)}</option>)}
        </select>
      );
    case "textarea":
      return <textarea className="o-input" rows={4} value={toInput(value, "text")} onChange={(e) => set(e.target.value)} />;
    case "ref":
      return <RefPicker value={(value as string) ?? null} onChange={set} targets={field.meta?.ref || []} labelField={field.labelField} />;
    case "file": {
      const b = value as BlobRef | null;
      return (
        <span className="o-file">
          {b && <span className="o-muted">{b.contentType} · {Math.ceil(b.size / 1024)} KB </span>}
          <input type="file" disabled={uploading} onChange={async (e) => {
            const file = e.target.files?.[0];
            if (!file) return;
            setUploading(true);
            try {
              set(await rt.client.upload({ type, field: field.name, feature: id, parent: id ? undefined : parent }, file, file.type || "application/octet-stream"));
            } catch (err) {
              rt.toast(describe(err, rt.t), "danger");
            } finally {
              setUploading(false);
            }
          }} />
        </span>
      );
    }
    case "datetime":
      return <input className="o-input" type="datetime-local" value={toInput(value, "datetime")} onChange={(e) => set(fromInput(e.target.value, "datetime"))} />;
    case "number":
      return <input className="o-input" type="number" value={toInput(value, "number")} onChange={(e) => set(fromInput(e.target.value, "number", field.meta))} />;
    case "decimal":
      return <input className="o-input" inputMode="decimal" value={toInput(value, "text")} onChange={(e) => set(e.target.value === "" ? null : e.target.value)} />;
  }
  return <input className="o-input" value={toInput(value, "text")} onChange={(e) => set(e.target.value === "" ? null : e.target.value)} />;
}

register("button", ({ node }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const fns = useFuncs();
  const page = usePage();
  const [busy, setBusy] = useState(false);
  const disabled = busy || ev.ex(node.disabled) === true;
  return (
    <button className={withClass(`o-button o-button-${node.variant ?? "secondary"}`, node)} disabled={disabled}
      onClick={async () => {
        setBusy(true);
        try {
          await runSteps(node.on?.click, ev.env, rt, page, fns);
        } finally {
          setBusy(false);
        }
      }}>
      {ev.text(node.label)}
    </button>
  );
});

register("menu", ({ node }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const fns = useFuncs();
  const page = usePage();
  const ref = useRef<HTMLDetailsElement>(null);
  const items = ((node.items as { label: string; visible?: string; steps: StepDoc[] }[]) || []).filter(
    (i) => i.visible === undefined || ev.ex(i.visible) === true);
  if (!items.length && !rt.editor) return null;
  return (
    <details className={withClass("o-menu", node)} ref={ref}>
      <summary className="o-button">{ev.text(node.label)}</summary>
      <div className="o-menu-items">
        {items.map((i, n) => (
          <button key={n} className="o-menu-item" onClick={async () => {
            ref.current?.removeAttribute("open");
            await runSteps(i.steps, ev.env, rt, page, fns);
          }}>{ev.text(i.label)}</button>
        ))}
      </div>
    </details>
  );
});

export type { JsonSchema };
