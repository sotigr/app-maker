import { useState } from "react";
import { Api } from "./api";

// UploadTarget says where uploaded blobs will be set (spec 14.4).
export type UploadTarget = { type: string; feature?: string; parent?: string };

// SchemaForm renders a form from a create or update view (JSON Schema,
// spec 13.3: forms are generated from the caller's views).
export function SchemaForm({ schema, initial, onSubmit, submitLabel, upload }: {
  schema: any;
  initial?: Record<string, any>;
  onSubmit: (data: Record<string, any>) => Promise<void>;
  submitLabel: string;
  upload?: UploadTarget;
}) {
  const props: Record<string, any> = schema?.properties || {};
  const required: string[] = schema?.required || [];
  const [values, setValues] = useState<Record<string, any>>(() => ({ ...(initial || {}) }));
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const names = Object.keys(props).sort();
  if (names.length === 0) return <p className="muted">Nothing writable for you here.</p>;
  const set = (k: string, v: any) => setValues((old) => ({ ...old, [k]: v }));
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setErr("");
        setBusy(true);
        try {
          const out: Record<string, any> = {};
          for (const k of names) {
            if (!(k in values)) continue;
            const v = values[k];
            if (initial && JSON.stringify(v) === JSON.stringify(initial[k])) continue; // merge patch: changes only
            out[k] = v === "" ? null : v;
          }
          await onSubmit(out);
          if (!initial) setValues({});
        } catch (x: any) {
          setErr(x?.message ? String(x.message) : String(x));
        } finally {
          setBusy(false);
        }
      }}
    >
      <div className="grid2">
        {names.map((k) => (
          <div key={k}>
            <label>
              {props[k].title || k}
              {required.includes(k) ? " *" : ""}
            </label>
            <Field schema={props[k]} value={values[k]} onChange={(v) => set(k, v)}
              upload={upload && { ...upload, field: k }} />
          </div>
        ))}
      </div>
      <p className="error">{err}</p>
      <button className="primary" disabled={busy} type="submit">
        {submitLabel}
      </button>
    </form>
  );
}

function baseType(s: any): string {
  if (Array.isArray(s.type)) return s.type[0];
  return s.type || (s.enum ? "enum" : "json");
}

function isBlob(s: any): boolean {
  const p = s?.properties;
  return !!p && Object.keys(p).length === 3 && "id" in p && "size" in p && "contentType" in p;
}

function BlobField({ value, onChange, target }: { value: any; onChange: (v: any) => void; target?: UploadTarget & { field: string } }) {
  const [state, setState] = useState("");
  if (!target) return <Value v={value} />;
  return (
    <div>
      {value?.id && <div className="muted"><Value v={value} /></div>}
      <input type="file" onChange={(e) => {
        const file = e.target.files?.[0];
        if (!file) return;
        setState("uploading…");
        Api.upload(target, file).then((b) => { onChange(b); setState(`uploaded ${file.name}; save to keep it`); },
          (x) => setState(String(x?.message || x)));
      }} />
      {state && <div className="muted">{state}</div>}
    </div>
  );
}

function Field({ schema, value, onChange, upload }: { schema: any; value: any; onChange: (v: any) => void; upload?: UploadTarget & { field: string } }) {
  if (isBlob(schema)) return <BlobField value={value} onChange={onChange} target={upload} />;
  const t = baseType(schema);
  if (schema.enum) {
    return (
      <select value={value ?? ""} onChange={(e) => onChange(e.target.value)}>
        <option value="">—</option>
        {schema.enum.filter((x: any) => x !== null).map((x: string) => (
          <option key={x}>{x}</option>
        ))}
      </select>
    );
  }
  switch (t) {
    case "boolean":
      return <input type="checkbox" checked={!!value} onChange={(e) => onChange(e.target.checked)} />;
    case "integer":
    case "number":
      return <input type="number" value={value ?? ""} onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))} />;
    case "string":
      if (schema.format === "date-time") {
        const local = value ? new Date(value).toISOString().slice(0, 16) : "";
        return <input type="datetime-local" value={local} onChange={(e) => onChange(e.target.value ? new Date(e.target.value + "Z").toISOString() : "")} />;
      }
      return (
        <input
          value={value ?? ""}
          placeholder={schema.format === "uuid" ? "feature id" : schema.pattern ? schema.pattern : ""}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    default:
      return (
        <textarea
          defaultValue={value === undefined ? "" : JSON.stringify(value, null, 2)}
          onBlur={(e) => {
            try {
              onChange(e.target.value.trim() ? JSON.parse(e.target.value) : "");
            } catch {
              /* keep the previous value until the JSON parses */
            }
          }}
        />
      );
  }
}

export function Value({ v }: { v: any }) {
  if (v === null || v === undefined) return <span className="muted">—</span>;
  if (typeof v === "object" && typeof v.url === "string") {
    const thumb = v.thumbnails && Object.values(v.thumbnails)[0];
    return (
      <a href={v.url} target="_blank" rel="noreferrer">
        {thumb ? <img src={thumb as string} alt="" style={{ maxHeight: 48, verticalAlign: "middle" }} /> : null} {v.contentType}, {Math.ceil(v.size / 1024)} KiB
      </a>
    );
  }
  if (typeof v === "object") return <span className="mono">{JSON.stringify(v)}</span>;
  if (typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-/.test(v)) return <span className="mono">{v.slice(0, 8)}…</span>;
  return <>{String(v)}</>;
}
