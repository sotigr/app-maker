// Data display components: table, list, field, preview, detail, stat,
// empty, transitions. Values render by their field kind from the bundle metadata.
import { useEffect, useState } from "react";
import { Node, register, WithScope, type ComponentProps } from "../render";
import { enumLabel, formatDate, formatNumber, makeEval, typeMeta, useEval, useFuncs, usePage, useRuntime, withClass } from "../runtime";
import { describe } from "../steps";
import type { FieldMeta, NodeDoc, Row } from "../types";

function get(row: Row | null | undefined, path: string): unknown {
  let cur: unknown = row;
  for (const p of path.split("/").slice(1)) {
    if (cur == null || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[p];
  }
  return cur;
}

export function labelOf(path: string, meta?: FieldMeta): string {
  if (meta?.title) return meta.title;
  const name = path.split("/").pop() || path;
  return name.replace(/([a-z])([A-Z])/g, "$1 $2").replace(/^./, (c) => c.toUpperCase());
}

export function RefLabel({ id }: { id: string }) {
  const rt = useRuntime();
  return <span className="o-ref">{rt.label(id) ?? "…"}</span>;
}

/** Value renders one field value by kind (or an explicit display). */
export function Value({ value, meta, display = "auto" }: { value: unknown; meta?: FieldMeta; display?: string }) {
  const rt = useRuntime();
  if (value == null || value === "") return <span className="o-muted">—</span>;
  const kind = display !== "auto" ? display : meta?.kind ?? typeof value;
  switch (kind) {
    case "ref":
      return typeof value === "string" ? <RefLabel id={value} /> : <span>{String(value)}</span>;
    case "date":
      return <span>{formatDate(value, "date")}</span>;
    case "datetime":
    case "time":
      return <span>{formatDate(value, kind === "time" ? "datetime" : "datetime")}</span>;
    case "enum":
    case "badge":
      return <span className={`o-badge o-badge-${String(value).replace(/[^a-z0-9_-]/gi, "")}`}>{enumLabel(meta, value)}</span>;
    case "bool":
    case "boolean":
      return <span>{rt.t(value === true ? "Yes" : "No")}</span>;
    case "int64":
    case "float":
    case "number":
      return <span className="o-num">{typeof value === "number" ? formatNumber(value, Number.isInteger(value) ? 0 : 2) : String(value)}</span>;
    case "decimal":
      return <span className="o-num">{String(value)}</span>;
    case "blob":
    case "image":
      return <BlobValue value={value as BlobValueDoc} />;
    case "list":
      return <span>{Array.isArray(value) ? value.map(String).join(", ") : String(value)}</span>;
    case "object":
    case "json":
      return <code className="o-json">{JSON.stringify(value)}</code>;
  }
  return <span>{String(value)}</span>;
}

type BlobValueDoc = { id?: string; url?: string; thumbnails?: Record<string, string>; contentType?: string; size?: number };

function blobLabel(b: BlobValueDoc, file: string): string {
  return `${b.contentType ?? file}${b.size ? ` · ${Math.ceil(b.size / 1024)} KB` : ""}`;
}

/** viewable reports whether the browser can show the file in the page. */
function viewable(contentType = ""): boolean {
  return (contentType.startsWith("image/") && contentType !== "image/svg+xml") || contentType === "application/pdf" ||
    contentType.startsWith("video/") || contentType.startsWith("audio/");
}

/**
 * BlobValue is a file link, with the smallest thumbnail when there is one.
 * Files the browser can show (images, PDF, video, audio) open in a modal
 * viewer; a modified click (new tab) and other files follow the link.
 */
function BlobValue({ value: b }: { value: BlobValueDoc }) {
  const rt = useRuntime();
  const [broken, setBroken] = useState(false);
  const [open, setOpen] = useState(false);
  if (!b.url) return <span className="o-muted">{rt.t("file")}</span>;
  // Reads list thumbnails only for types that get them, in size order.
  const thumb = !broken && b.thumbnails ? Object.values(b.thumbnails)[0] : undefined;
  const onClick = (e: React.MouseEvent) => {
    if (!viewable(b.contentType) || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    e.stopPropagation(); // rows with rowPage navigate on click
    setOpen(true);
  };
  return (
    <>
      <a className="o-blob" href={b.url} target="_blank" rel="noopener noreferrer" onClick={onClick}>
        {thumb ? <img src={thumb} alt="" onError={() => setBroken(true)} /> : blobLabel(b, rt.t("file"))}
      </a>
      {open && <FileModal value={b} onClose={() => setOpen(false)} />}
    </>
  );
}

/**
 * FileView draws a file the browser can show; onFail is called when an
 * image or media URL fails (e.g. an expired signature).
 */
function FileView({ url, contentType, className, height, onFail }: {
  url: string; contentType: string; className: string; height: number | string; onFail?: () => void;
}) {
  const rt = useRuntime();
  const ct = contentType;
  if (ct.startsWith("image/") && ct !== "image/svg+xml") return <img className={className} src={url} alt="" onError={onFail} />;
  if (ct === "application/pdf") return <iframe className={className} src={url} title={rt.t("PDF preview")} style={{ height }} />;
  if (ct.startsWith("video/")) return <video className={className} src={url} controls preload="metadata" style={{ maxHeight: height }} onError={onFail} />;
  if (ct.startsWith("audio/")) return <audio className={className} src={url} controls preload="metadata" onError={onFail} />;
  return null;
}

/** FileModal shows a file over the page; Escape or the backdrop closes it. */
function FileModal({ value: b, onClose }: { value: BlobValueDoc; onClose: () => void }) {
  const rt = useRuntime();
  useEffect(() => {
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [onClose]);
  if (!b.url) return null;
  return (
    // Clicks stay in the viewer: React passes them to the row or card
    // around the link, which may navigate or start a drag.
    <div className="o-dialog-backdrop" onClick={(e) => { e.stopPropagation(); if (e.target === e.currentTarget) onClose(); }}
      onPointerDown={(e) => e.stopPropagation()}>
      <div role="dialog" aria-modal="true" className="o-dialog o-file-modal">
        <div className="o-dialog-head">
          <span className="o-muted">{blobLabel(b, rt.t("file"))}</span>
          <span className="o-file-modal-actions">
            <a className="o-button o-button-link" href={b.url} target="_blank" rel="noopener noreferrer">{rt.t("Open in new tab")}</a>
            <button className="o-button o-button-link" aria-label={rt.t("Close")} onClick={onClose}>×</button>
          </span>
        </div>
        <FileView url={b.url} contentType={b.contentType ?? ""} className="o-preview" height="75vh" />
      </div>
    </div>
  );
}

function useList(source: unknown): { rows: Row[] | null; type?: string } {
  const ev = useEval();
  const data = (ev.env.data || {}) as Record<string, unknown>;
  const rows = data[source as string] as Row[] | null | undefined;
  return { rows: rows ?? null, type: rows?.[0]?.type };
}

/** useOne resolves a feature-shaped component's feature: its source, or item. */
function useOne(node: NodeDoc): Row | null {
  const ev = useEval();
  if (typeof node.source === "string") return ((ev.env.data as Record<string, unknown>)?.[node.source] as Row | null) ?? null;
  return (ev.env.item as Row | undefined) ?? null;
}

function sourceType(node: NodeDoc, page: { name: string }, rt: ReturnType<typeof useRuntime>): string | undefined {
  const p = rt.bundle.pages.find((x) => x.name === page.name);
  const s = p?.doc.data?.[node.source as string];
  return s?.type ?? s?.query?.type;
}

function Loading() {
  return <div className="o-loading" aria-busy="true" />;
}

register("table", ({ node, path }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const fns = useFuncs();
  const page = usePage();
  const { rows } = useList(node.source);
  const [shown, setShown] = useState(Number(node.pageSize) || 50);
  const meta = typeMeta(rt, sourceType(node, page, rt));
  const cols = (node.columns as { field?: string; value?: string; label?: string; display?: string; page?: string; params?: Record<string, unknown> }[]) || [];
  const className = withClass("o-table-wrap", node);
  if (!rows) return <Loading />;
  if (!rows.length) return <div className="o-empty">{ev.text(node.empty) || rt.t("Nothing here yet.")}</div>;
  const rowEnv = (item: Row, index: number) => ({ ...ev.env, item, index });
  const open = (item: Row, index: number) => {
    if (typeof node.rowPage !== "string") return;
    const env = rowEnv(item, index);
    const params: Record<string, unknown> = {};
    for (const [k, v] of Object.entries((node.rowParams as Record<string, unknown>) || {})) {
      params[k] = evalIn(env, v, fns);
    }
    rt.navigate(node.rowPage, params);
  };
  return (
    <div className={className}>
      <table className={`o-table${node.rowPage ? " o-table-click" : ""}`}>
        <thead>
          <tr>
            {cols.map((c, i) => (
              <th key={i}>{c.label !== undefined ? ev.text(c.label) : c.field ? labelOf(c.field, meta?.fields[c.field]) : ""}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.slice(0, shown).map((item, index) => (
            <tr key={item.id} onClick={() => open(item, index)}>
              <WithScope vars={{ item, index }}>
                {cols.map((c, i) => (
                  <td key={i}>
                    <Cell col={c} item={item} meta={c.field ? meta?.fields[c.field] : undefined} path={`${path}/columns/${i}`} />
                  </td>
                ))}
              </WithScope>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length > shown && (
        <button className="o-button o-button-link" onClick={() => setShown(shown + (Number(node.pageSize) || 50))}>{rt.t("Show more")}</button>
      )}
    </div>
  );
});

function evalIn(env: Record<string, unknown>, v: unknown, fns: ReturnType<typeof useFuncs>): unknown {
  return makeEval(env, fns).ex(v);
}

function Cell({ col, item, meta }: { col: { field?: string; value?: string; display?: string; page?: string; params?: Record<string, unknown> }; item: Row; meta?: FieldMeta; path: string }) {
  const ev = useEval();
  const rt = useRuntime();
  const value = col.field ? get(item, col.field) : ev.ex(col.value);
  const shown = <Value value={value} meta={meta} display={col.display} />;
  if (!col.page) return shown;
  const params: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(col.params || {})) params[k] = ev.ex(v);
  return (
    <a href={rt.href(col.page, params)} onClick={(e) => {
      e.preventDefault();
      e.stopPropagation();
      rt.navigate(col.page!, params);
    }}>{shown}</a>
  );
}

register("list", ({ node, path }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const { rows } = useList(node.source);
  const className = withClass("o-list", node);
  if (!rows) return <Loading />;
  if (!rows.length) return <div className="o-empty">{ev.text(node.empty) || rt.t("Nothing here yet.")}</div>;
  return (
    <div className={className}>
      {rows.map((item, index) => (
        <div className="o-list-item" key={item.id}>
          <WithScope vars={{ item, index }}>
            <Node node={node.item as NodeDoc} path={`${path}/item`} />
          </WithScope>
        </div>
      ))}
    </div>
  );
});

register("field", ({ node }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const f = useOne(node);
  const path = node.path as string;
  const meta = typeMeta(rt, f?.type)?.fields[path];
  const label = node.label !== undefined ? ev.text(node.label) : "";
  return (
    <div className={withClass("o-field", node)}>
      {label && <span className="o-field-label">{label}</span>}
      <Value value={get(f, path)} meta={meta} display={node.display as string | undefined} />
    </div>
  );
});

// preview shows a file in the page: the browser draws images, PDFs, video,
// and audio from the signed URL (served inline, sandboxed); other types
// fall back to the file link.
register("preview", ({ node }: ComponentProps) => {
  const f = useOne(node);
  const b = get(f, node.path as string) as BlobValueDoc | null | undefined;
  // Live updates re-sign URLs; keep the first one per file so playback
  // and scrolling survive, and move to the newest only when it fails.
  const [shown, setShown] = useState<{ id?: string; url?: string; broken?: boolean }>({});
  if (b?.id !== shown.id) setShown({ id: b?.id, url: b?.url });
  if (!b?.url) return null;
  const url = shown.id === b.id && shown.url ? shown.url : b.url;
  const failed = () => setShown(url !== b.url ? { id: b.id, url: b.url } : { id: b.id, url, broken: true });
  const className = withClass("o-preview", node);
  const ct = b.contentType ?? "";
  const height = Number(node.height) || 480;
  // display "modal": a thumbnail or file link that opens the viewer.
  if (shown.broken || node.display === "modal" || !viewable(ct)) return <div className={className}><BlobValue value={b} /></div>;
  return <FileView url={url} contentType={ct} className={className} height={height} onFail={failed} />;
});

register("detail", ({ node }: ComponentProps) => {
  const rt = useRuntime();
  const f = useOne(node);
  const className = withClass("o-detail", node);
  if (!f) return <Loading />;
  const meta = typeMeta(rt, f.type);
  const paths = (node.fields as string[] | undefined) || Object.keys(meta?.fields || {});
  return (
    <dl className={className}>
      {paths.filter((p) => !meta || p in meta.fields).map((p) => (
        <div key={p} className="o-detail-row">
          <dt>{labelOf(p, meta?.fields[p])}</dt>
          <dd><Value value={get(f, p)} meta={meta?.fields[p]} /></dd>
        </div>
      ))}
    </dl>
  );
});

register("stat", ({ node }: ComponentProps) => {
  const ev = useEval();
  const v = ev.ex(node.value);
  const shown = v == null ? "—" : node.format === "percent" ? `${formatNumber(Number(v) * 100)}%`
    : node.format === "date" ? formatDate(v) : typeof v === "number" ? formatNumber(v, Number.isInteger(v) ? 0 : 1) : String(v);
  return (
    <div className={withClass("o-stat", node)}>
      <div className="o-stat-value">{shown}</div>
      <div className="o-stat-label">{ev.text(node.label)}</div>
    </div>
  );
});

register("empty", ({ node }: ComponentProps) => {
  const ev = useEval();
  return <div className={withClass("o-empty", node)}>{ev.text(node.text)}</div>;
});

register("transitions", ({ node }: ComponentProps) => {
  const rt = useRuntime();
  const f = useOne(node);
  const [busy, setBusy] = useState(false);
  const className = withClass("o-row o-gap-sm", node);
  const meta = typeMeta(rt, f?.type);
  if (!f || !meta?.stateMachines) return null;
  const field = (node.field as string | undefined) || Object.keys(meta.stateMachines)[0];
  const sm = meta.stateMachines[field];
  const from = get(f, field);
  const actions = sm.transitions.filter((t) => t.from === from && rt.can(f, `action:${t.action}`));
  return (
    <div className={className}>
      {actions.map((t) => (
        <button key={t.action} className="o-button" disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await rt.client.action(f.type, f.id, t.action, {});
            } catch (e) {
              rt.toast(describe(e, rt.t), "danger");
            } finally {
              setBusy(false);
            }
          }}>
          {meta.actionLabels?.[t.action] ?? labelOf("/" + t.action)}
        </button>
      ))}
    </div>
  );
});
