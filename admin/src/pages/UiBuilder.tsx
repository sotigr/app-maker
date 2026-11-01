// The visual UI builder (ADR 0016): palette and outline on the left, a live
// preview drawn by the real renderer (@organon/ui) in the middle, and an
// inspector generated from the component catalog on the right. Saving
// writes ui/pages/<name>.json through the dev-only endpoint; the files stay
// the source of truth and can be edited by hand or by agents.
import { useEffect, useMemo, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { OrganonClient } from "@organon/client";
import { PageView, RuntimeProvider, resolve, type Bundle, type PageDTO } from "@organon/ui";
import baseCss from "@organon/ui/base.css?inline";
import { Api, ApiError, describeError, type UiCatalog, type UiProblem, type UiProp, type UiSource, type UiStep } from "../api";
import { blank, component, getAt, insertAt, move, nodeFor, outline, parentList, removeAt, setAt, slots, summary, type Doc } from "../uitree";

const NEW_PAGE = (name: string): Doc => ({
  page: name, title: "New page", route: `/${name.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase())}`,
  body: { component: "stack", children: [{ component: "heading", text: "New page", level: 1 }] },
});

const client = new OrganonClient({ baseUrl: "/" });

interface Ctx {
  src: UiSource;
  cat: UiCatalog;
  doc: Doc;
  ptr: string;
  /** typeOf gives the feature type props at this node refer to. */
  type?: string;
}

/** typeAt finds the type a node's paths and actions refer to: its source,
 * its type prop, or the source of the row component around it. */
function typeAt(doc: Doc, ptr: string): string | undefined {
  const sourceType = (name: unknown) => {
    const s = doc.data?.[name as string];
    return s?.type ?? s?.query?.type;
  };
  const node = getAt(doc, ptr);
  if (typeof node?.type === "string") return node.type;
  if (typeof node?.source === "string") return sourceType(node.source);
  let p = ptr;
  while (p.includes("/")) {
    p = p.slice(0, p.lastIndexOf("/"));
    const n = getAt(doc, p);
    if (n?.component && typeof n.source === "string" && ["table", "list", "board"].includes(n.component)) return sourceType(n.source);
  }
  return undefined;
}

export function UiBuilder({ name }: { name: string }) {
  const [src, setSrc] = useState<UiSource>();
  const [cat, setCat] = useState<UiCatalog>();
  const [doc, setDoc] = useState<Doc>();
  const [origName, setOrigName] = useState<string | undefined>(name === "new" ? undefined : name);
  const [sel, setSel] = useState("/body");
  const [problems, setProblems] = useState<UiProblem[]>([]);
  const [status, setStatus] = useState("");
  const [dirty, setDirty] = useState(false);
  const [view, setView] = useState<"preview" | "json">("preview");
  const [params, setParams] = useState<Record<string, string>>({});
  const [as, setAs] = useState("me");
  const [me, setMe] = useState<{ id: string; kind: string; roles: string[] } | null>(null);
  const [drag, setDrag] = useState<string | null>(null);

  useEffect(() => {
    Promise.all([Api.uiSource(), Api.uiCatalog(), Api.session()]).then(([s, c, sess]) => {
      setSrc(s);
      setCat(c);
      setMe(sess.principal);
      const p = s.pages.find((x) => x.name === name);
      setDoc(p ? p.doc : NEW_PAGE(name === "new" ? "newPage" : name));
      if (!p) setDirty(true);
    }, (e) => setStatus(describeError(e)));
  }, [name]);

  const file = useMemo(() => src?.pages.find((p) => p.name === origName)?.file ?? `ui/pages/${doc?.page}.json`, [src, origName, doc?.page]);
  const entries = useMemo(() => (doc ? outline(cat, doc) : []), [cat, doc]);

  // Validate as you type, with the same compiler as organon validate.
  useEffect(() => {
    if (!doc || !src) return;
    const t = setTimeout(() => {
      const files: Record<string, string | null> = { [file]: JSON.stringify(doc) };
      if (origName && origName !== doc.page) {
        const old = src.pages.find((p) => p.name === origName)?.file;
        if (old && old !== file) files[old] = null;
      }
      Api.validateUi(files).then((r) => setProblems(r.problems), () => undefined);
    }, 400);
    return () => clearTimeout(t);
  }, [doc, file]);

  if (!doc || !src || !cat) return <p className={status ? "error" : "muted"}>{status || "Loading…"}</p>;

  const update = (d: Doc) => {
    setDoc(d);
    setDirty(true);
    setStatus("");
  };
  const mine = problems.filter((p) => p.file === file);
  const others = problems.filter((p) => p.file !== file && !p.warning);
  const bad = new Set(mine.filter((p) => !p.warning).map((p) => nodeFor(entries, p.pointer || "") || "").filter(Boolean));
  const sources = Object.entries(doc.data || {}).map(([n, s]: [string, any]) => ({ name: n, list: s.feature === undefined }));
  const fieldsOf = (t?: string) => Object.keys((t && src.types[t]?.fields) || {});

  const addComponent = (c: string) => {
    const comp = component(cat, c)!;
    const node = blank(comp, sources, fieldsOf(typeAt(doc, sel)));
    const target = getAt(doc, sel);
    const inner = slots(cat, target, sel).find((s) => s.many);
    let d: Doc, at: string;
    if (inner) {
      const n = (getAt(doc, inner.ptr) || []).length;
      d = insertAt(doc, inner.ptr, n, node);
      at = `${inner.ptr}/${n}`;
    } else if (parentList(sel)) {
      const pl = parentList(sel)!;
      d = insertAt(doc, pl.list, pl.index + 1, node);
      at = `${pl.list}/${pl.index + 1}`;
    } else return;
    update(d);
    setSel(at);
  };
  const remove = (ptr: string) => {
    if (ptr === "/body" || !parentList(ptr)) return;
    update(removeAt(doc, ptr));
    setSel(parentList(ptr)!.list.replace(/\/[^/]+$/, "") || "/body");
  };
  const shift = (ptr: string, by: number) => {
    const pl = parentList(ptr);
    if (!pl) return;
    const n = (getAt(doc, pl.list) as any[]).length;
    const to = pl.index + by;
    if (to < 0 || to >= n) return;
    update(move(doc, ptr, pl.list, by > 0 ? to + 1 : to));
    setSel(`${pl.list}/${to}`);
  };
  const duplicate = (ptr: string) => {
    const pl = parentList(ptr);
    if (!pl) return;
    update(insertAt(doc, pl.list, pl.index + 1, structuredClone(getAt(doc, ptr))));
    setSel(`${pl.list}/${pl.index + 1}`);
  };
  const dropOn = (target: string, inside: boolean) => {
    if (!drag) return;
    const node = getAt(doc, target);
    const slot = inside ? slots(cat, node, target).find((s) => s.many) : undefined;
    if (drag.startsWith("new:")) {
      const comp = component(cat, drag.slice(4))!;
      const n = blank(comp, sources, fieldsOf(typeAt(doc, target)));
      if (slot) update(insertAt(doc, slot.ptr, (getAt(doc, slot.ptr) || []).length, n));
      else if (parentList(target)) update(insertAt(doc, parentList(target)!.list, parentList(target)!.index, n));
    } else if (drag !== target && !target.startsWith(drag + "/")) {
      if (slot) update(move(doc, drag, slot.ptr, (getAt(doc, slot.ptr) || []).length));
      else if (parentList(target)) update(move(doc, drag, parentList(target)!.list, parentList(target)!.index));
    }
    setDrag(null);
  };

  const save = async () => {
    setStatus("Saving…");
    try {
      const r = await Api.savePage(doc.page, doc, origName && origName !== doc.page ? origName : undefined);
      setDirty(false);
      setProblems(r.problems || []);
      setStatus(`Saved ${r.saved.join(", ")}; organon dev reloads it.`);
      if (origName !== doc.page) {
        setOrigName(doc.page);
        location.hash = `#/ui/pages/${encodeURIComponent(doc.page)}`;
      }
    } catch (e) {
      if (e instanceof ApiError && e.problem.problems) setProblems(e.problem.problems as UiProblem[]);
      setStatus(e instanceof ApiError && e.problem.code === "not_found" ? "Saving needs organon dev." : `Not saved: ${describeError(e)}`);
    }
  };
  const del = async () => {
    if (!origName || !confirm(`Delete page ${origName}?`)) return;
    try {
      await Api.deletePage(origName);
      location.hash = "#/ui";
    } catch (e) {
      if (e instanceof ApiError && e.problem.problems) setProblems(e.problem.problems as UiProblem[]);
      setStatus(`Not deleted: ${describeError(e)}`);
    }
  };

  // The preview's bundle: every page as written, this one as edited.
  const dto: PageDTO = { name: doc.page, route: doc.route, params: doc.params || {}, doc: doc as any };
  const bundle: Bundle = {
    version: "preview", loginRequired: false, types: src.types,
    app: src.app || { home: doc.page },
    pages: [...src.pages.filter((p) => p.name !== origName && p.name !== doc.page).map((p) => ({ name: p.name, route: p.doc.route, params: p.doc.params || {}, doc: p.doc })), dto],
  };
  const principal = as === "me" ? me : as === "anonymous" ? null : { id: me?.id || "", kind: "user", roles: as === "user" ? [] : as.split(",").map((s) => s.trim()) };
  const typed: Record<string, unknown> = {};
  for (const [k, spec] of Object.entries((doc.params || {}) as Record<string, { type: string }>)) {
    const v = params[k];
    if (v === undefined || v === "") continue;
    typed[k] = spec.type === "integer" || spec.type === "number" ? Number(v) : spec.type === "boolean" ? v === "true" : v;
  }
  const missing = Object.keys(doc.params || {}).filter((k) => doc.route?.includes(`:${k}`) && !params[k]);

  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}><a href="#/ui">UI</a> / {doc.page}{dirty ? " •" : ""}</h1>
        <div className="row">
          <button onClick={() => setView(view === "json" ? "preview" : "json")}>{view === "json" ? "Preview" : "JSON"}</button>
          {origName && <button className="danger" onClick={del} disabled={!src.dev}>Delete</button>}
          <button className="primary" onClick={save} disabled={!src.dev || !dirty} title={src.dev ? "" : "Saving needs organon dev"}>Save</button>
        </div>
      </div>
      {!src.dev && <p className="muted">Read-only: this server is not running under organon dev. Change ui/ files in the manifest and deploy with organon plan / apply.</p>}
      <p className={status.startsWith("Not") ? "error" : "okmsg"}>{status}</p>
      <div className="ui-editor">
        <aside className="palette">
          <div className="kind">Page</div>
          <div className={`ui-outline-item${sel === "" ? " selected" : ""}`} onClick={() => setSel("")}>⚙ page settings</div>
          <div className="kind">Outline</div>
          {entries.map((e) => (
            <div key={e.ptr} className={`ui-outline-item${sel === e.ptr ? " selected" : ""}${bad.has(e.ptr) ? " bad" : ""}`}
              style={{ paddingLeft: 6 + e.depth * 12 }} draggable={e.ptr !== "/body"}
              onDragStart={(ev) => { ev.stopPropagation(); setDrag(e.ptr); }}
              onDragOver={(ev) => ev.preventDefault()}
              onDrop={(ev) => {
                ev.preventDefault();
                // The right half of a container's row drops inside it; elsewhere before it.
                const el = ev.currentTarget as HTMLElement;
                dropOn(e.ptr, ev.nativeEvent.offsetX > el.offsetWidth / 2 && slots(cat, e.node, e.ptr).some((s) => s.many));
              }}
              onClick={() => setSel(e.ptr)} title={e.ptr}>
              {e.slot && <span className="muted">{e.slot}: </span>}
              <b>{e.node.component}</b> <span className="muted">{summary(e.node)}</span>
            </div>
          ))}
          <p className="muted small">Drag to reorder; drop on the right half of a container to nest.</p>
          {cat.families.map((f) => (
            <div key={f}>
              <div className="kind">{f}</div>
              {cat.components.filter((c) => c.family === f).map((c) => (
                <div key={c.name} className="item" draggable title={c.doc}
                  onDragStart={() => setDrag("new:" + c.name)} onClick={() => addComponent(c.name)}>{c.name}</div>
              ))}
            </div>
          ))}
        </aside>
        <section className="ui-center">
          {view === "json" ? (
            <JsonDoc doc={doc} onApply={(d) => update(d)} />
          ) : (
            <>
              <div className="row ui-preview-bar">
                {Object.keys(doc.params || {}).map((k) => (
                  <ParamInput key={k} name={k} doc={doc} value={params[k] || ""} onChange={(v) => setParams({ ...params, [k]: v })} />
                ))}
                <label className="inline">as <select value={as} onChange={(e) => setAs(e.target.value)}>
                  <option value="me">me (admin)</option><option value="user">a user without roles</option>
                  <option value="editor">a user with role editor</option><option value="anonymous">anonymous</option>
                </select></label>
                <span className="muted small">Data is read as you; “as” changes only principal in expressions.</span>
              </div>
              {missing.length ? <p className="muted">Pick {missing.join(", ")} to preview this page.</p> : (
                <PreviewFrame bundle={bundle} dto={dto} params={typed} principal={principal} styles={src.styles || ""}
                  selected={sel} bad={bad} onSelect={setSel}
                  onNavigate={(path) => {
                    const [p, q = ""] = path.split("?");
                    const r = resolve(bundle, p, q);
                    if (r && r.dto.name !== doc.page && (!dirty || confirm("Leave without saving?"))) location.hash = `#/ui/pages/${encodeURIComponent(r.dto.name)}`;
                  }} />
              )}
            </>
          )}
          <div className="problems card">
            {mine.length === 0 && others.length === 0 ? <span className="okmsg">No problems.</span> : null}
            {mine.map((p, i) => (
              <div key={i} className={p.warning ? "muted" : "error"} onClick={() => setSel(nodeFor(entries, p.pointer || "") ?? "")} style={{ cursor: "pointer" }}>
                <span className="mono">{p.pointer || "/"}</span> {p.message}
              </div>
            ))}
            {others.map((p, i) => <div key={"o" + i} className="error"><span className="mono">{p.file}#{p.pointer}</span> {p.message}</div>)}
          </div>
        </section>
        <aside className="inspector">
          {sel === "" ? <PageInspector ctx={{ src, cat, doc, ptr: "" }} update={update} /> : (
            <NodeInspector ctx={{ src, cat, doc, ptr: sel, type: typeAt(doc, sel) }} update={update}
              remove={() => remove(sel)} up={() => shift(sel, -1)} down={() => shift(sel, 1)} dup={() => duplicate(sel)} />
          )}
        </aside>
      </div>
    </>
  );
}

/** ParamInput edits a preview parameter, offering features of the type a
 * data source loads with it ("feature": "params.id"). */
function ParamInput({ name, doc, value, onChange }: { name: string; doc: Doc; value: string; onChange(v: string): void }) {
  const [options, setOptions] = useState<{ id: string; label: string }[]>([]);
  const type = Object.values(doc.data || {}).find((s: any) => (s.feature === `params.${name}` || s.children === `params.${name}`) && s.type) as any;
  useEffect(() => {
    if (!type) return;
    Api.query({ type: type.type, limit: 25 }).then((r) => {
      const opts = r.items.map((f: any) => ({ id: f.id, label: f.data?.title ?? f.data?.name ?? f.data?.displayName ?? f.id.slice(0, 8) }));
      setOptions(opts);
      if (!value && opts[0]) onChange(opts[0].id);
    }, () => undefined);
  }, [type?.type]);
  return (
    <label className="inline">{name}{" "}
      {options.length ? (
        <select value={value} onChange={(e) => onChange(e.target.value)}>
          {options.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
        </select>
      ) : <input value={value} onChange={(e) => onChange(e.target.value)} style={{ width: 220 }} />}
    </label>
  );
}

interface PreviewProps {
  bundle: Bundle;
  dto: PageDTO;
  params: Record<string, unknown>;
  principal: { id: string; kind: string; roles: string[] } | null;
  styles: string;
  selected: string;
  bad: Set<string>;
  onSelect(ptr: string): void;
  onNavigate(path: string): void;
}

/** PreviewFrame renders the page with the real renderer in an iframe, so
 * the app's styles.css cannot restyle the console and vice versa. */
function PreviewFrame(p: PreviewProps) {
  const frame = useRef<HTMLIFrameElement>(null);
  const root = useRef<Root | null>(null);
  const [ready, setReady] = useState(false);
  useEffect(() => {
    const d = frame.current!.contentDocument!;
    d.open();
    d.write('<!doctype html><html><head><meta charset="utf-8"><style id="base"></style><style id="app"></style></head><body><div id="root"></div></body></html>');
    d.close();
    d.getElementById("base")!.textContent = baseCss;
    root.current = createRoot(d.getElementById("root")!);
    setReady(true);
    return () => {
      const r = root.current;
      root.current = null;
      setTimeout(() => r?.unmount());
    };
  }, []);
  useEffect(() => {
    const d = frame.current?.contentDocument;
    if (d?.getElementById("app")) d.getElementById("app")!.textContent = p.styles;
  }, [p.styles, ready]);
  const editor = useMemo(() => ({ selected: p.selected, onSelect: p.onSelect, problems: p.bad }), [p.selected, p.bad]);
  useEffect(() => {
    root.current?.render(
      <RuntimeProvider client={client} bundle={p.bundle} principal={p.principal} editor={editor} navigate={p.onNavigate}>
        <div className="o-main"><PageView dto={p.dto} params={p.params} setTitle={false} /></div>
      </RuntimeProvider>,
    );
  });
  useEffect(() => {
    frame.current?.contentDocument?.querySelector(`[data-ui-path="${p.selected}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [p.selected]);
  return <iframe ref={frame} className="ui-preview" title="Preview" />;
}

function JsonDoc({ doc, onApply }: { doc: Doc; onApply(d: Doc): void }) {
  const [text, setText] = useState(() => JSON.stringify(doc, null, 2));
  const [err, setErr] = useState("");
  useEffect(() => setText(JSON.stringify(doc, null, 2)), [doc]);
  return (
    <div className="ui-json">
      <textarea value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
      <div className="row">
        <button onClick={() => { try { onApply(JSON.parse(text)); setErr(""); } catch (e) { setErr(String(e)); } }}>Apply JSON</button>
        <span className="error">{err}</span>
      </div>
    </div>
  );
}

function NodeInspector({ ctx, update, remove, up, down, dup }: { ctx: Ctx; update(d: Doc): void; remove(): void; up(): void; down(): void; dup(): void }) {
  const node = getAt(ctx.doc, ctx.ptr);
  if (!node) return <p className="muted">Select a component.</p>;
  const c = component(ctx.cat, node.component);
  const set = (k: string, v: any) => update(setAt(ctx.doc, `${ctx.ptr}/${k}`, v === "" ? undefined : v));
  return (
    <div>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <h2 style={{ margin: 0 }}>{node.component}</h2>
        <div className="row" style={{ gap: 4 }}>
          <button title="Move up" onClick={up}>↑</button><button title="Move down" onClick={down}>↓</button>
          <button title="Duplicate" onClick={dup}>⧉</button>
          <button className="danger" title="Delete" onClick={remove} disabled={ctx.ptr === "/body"}>✕</button>
        </div>
      </div>
      <p className="muted small">{c?.doc} <span className="mono">{ctx.ptr}</span></p>
      <label>component</label>
      <select value={node.component} onChange={(e) => set("component", e.target.value)}>
        {ctx.cat.components.map((x) => <option key={x.name}>{x.name}</option>)}
      </select>
      {(c?.props || []).filter((p) => p.kind !== "children" && p.kind !== "node").map((p) => (
        <PropField key={p.name} prop={p} value={node[p.name]} ctx={ctx} onChange={(v) => set(p.name, v)} />
      ))}
      {ctx.cat.common.map((p) => <PropField key={p.name} prop={p} value={node[p.name]} ctx={ctx} onChange={(v) => set(p.name, v)} />)}
      {(c?.events || []).map((ev) => (
        <div key={ev}>
          <label>on {ev}</label>
          <StepsEditor steps={node.on?.[ev] || []} ctx={ctx} onChange={(s) => set("on", { ...(node.on || {}), [ev]: s })} />
        </div>
      ))}
    </div>
  );
}

const hint: Record<string, string> = {
  text: "text; {{ expr }} inserts values",
  expr: "expression; 'quoted' text",
  path: "field, e.g. /title",
};

function PropField({ prop, value, ctx, onChange }: { prop: UiProp; value: any; ctx: Ctx; onChange(v: any): void }) {
  const types = Object.keys(ctx.src.types).sort();
  const pages = ctx.src.pages.map((p) => p.name).concat(ctx.src.pages.some((p) => p.name === ctx.doc.page) ? [] : [ctx.doc.page]);
  const t = ctx.type;
  const label = <label title={prop.doc}>{prop.name}{prop.required ? " *" : ""} <span className="muted small">{hint[prop.kind] || prop.kind}</span></label>;
  const listId = `dl-${prop.name}-${ctx.ptr}`;
  switch (prop.kind) {
    case "enum":
      return <>{label}<select value={value ?? ""} onChange={(e) => onChange(e.target.value)}><option value=""></option>{prop.enum?.map((x) => <option key={x}>{x}</option>)}</select></>;
    case "bool":
      return <label className="inline"><input type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked || undefined)} /> {prop.name}</label>;
    case "int":
      return <>{label}<input type="number" value={value ?? ""} onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))} /></>;
    case "type":
      return <>{label}<select value={value ?? ""} onChange={(e) => onChange(e.target.value)}><option value=""></option>{types.map((x) => <option key={x}>{x}</option>)}</select></>;
    case "page":
      return <>{label}<select value={value ?? ""} onChange={(e) => onChange(e.target.value)}><option value=""></option>{pages.map((x) => <option key={x}>{x}</option>)}</select></>;
    case "source":
      return <>{label}<select value={value ?? ""} onChange={(e) => onChange(e.target.value)}><option value=""></option>{Object.keys(ctx.doc.data || {}).map((x) => <option key={x}>{x}</option>)}</select></>;
    case "action": {
      const acts = Object.keys(ctx.src.types[t || ""]?.actions || {});
      return <>{label}<select value={value ?? ""} onChange={(e) => onChange(e.target.value)}><option value=""></option>{acts.map((x) => <option key={x}>{x}</option>)}</select></>;
    }
    case "path":
      return (
        <>{label}<input className="mono" list={listId} value={value ?? ""} onChange={(e) => onChange(e.target.value)} />
          <datalist id={listId}>{Object.keys(ctx.src.types[t || ""]?.fields || {}).map((f) => <option key={f} value={f} />)}</datalist></>
      );
    case "paths":
      return <>{label}<input className="mono" value={(value || []).join(", ")} onChange={(e) => onChange(e.target.value ? e.target.value.split(",").map((s) => s.trim()).filter(Boolean) : undefined)} placeholder="/title, /status" /></>;
    case "params":
    case "set":
      return <>{label}<MapEditor value={value || {}} onChange={(m) => onChange(Object.keys(m).length ? m : undefined)} /></>;
    case "steps":
      return <>{label}<StepsEditor steps={value || []} ctx={ctx} onChange={onChange} /></>;
    case "objects":
      return <>{label}<ObjectsEditor prop={prop} value={value || []} ctx={ctx} onChange={onChange} /></>;
    case "expr":
      return <>{label}<input className="mono" value={value === undefined ? "" : String(value)} onChange={(e) => onChange(e.target.value)} /></>;
  }
  return <>{label}<input value={value ?? ""} onChange={(e) => onChange(e.target.value)} /></>;
}

function MapEditor({ value, onChange }: { value: Record<string, any>; onChange(v: Record<string, any>): void }) {
  const entries = Object.entries(value);
  return (
    <div className="ui-map">
      {entries.map(([k, v], i) => (
        <div className="row" key={i} style={{ gap: 4, flexWrap: "nowrap" }}>
          <input className="mono" value={k} style={{ width: "40%" }} onChange={(e) => onChange(Object.fromEntries(entries.map(([kk, vv], j) => (j === i ? [e.target.value, vv] : [kk, vv]))))} />
          <input className="mono" value={String(v)} onChange={(e) => onChange({ ...value, [k]: e.target.value })} />
          <button onClick={() => { const n = { ...value }; delete n[k]; onChange(n); }}>✕</button>
        </div>
      ))}
      <button className="link" onClick={() => onChange({ ...value, [`key${entries.length + 1}`]: "" })}>+ entry</button>
    </div>
  );
}

function ObjectsEditor({ prop, value, ctx, onChange }: { prop: UiProp; value: any[]; ctx: Ctx; onChange(v: any[]): void }) {
  const set = (i: number, k: string, v: any) => onChange(value.map((it, j) => {
    if (j !== i) return it;
    const n = { ...it };
    if (v === undefined || v === "") delete n[k];
    else n[k] = v;
    return n;
  }));
  return (
    <div className="ui-objects">
      {value.map((it, i) => (
        <div key={i} className="ui-object">
          <div className="row" style={{ justifyContent: "space-between" }}>
            <span className="muted small">{prop.name} {i + 1}</span>
            <span className="row" style={{ gap: 2 }}>
              <button disabled={i === 0} onClick={() => { const n = [...value]; [n[i - 1], n[i]] = [n[i], n[i - 1]]; onChange(n); }}>↑</button>
              <button onClick={() => onChange(value.filter((_, j) => j !== i))}>✕</button>
            </span>
          </div>
          {(prop.item || []).filter((p) => p.kind !== "children").map((p) => (
            <PropField key={p.name} prop={p} value={it[p.name]} ctx={ctx} onChange={(v) => set(i, p.name, v)} />
          ))}
          {(prop.item || []).some((p) => p.kind === "children") && <p className="muted small">Edit its children in the outline.</p>}
        </div>
      ))}
      <button className="link" onClick={() => onChange([...value, Object.fromEntries((prop.item || []).filter((p) => p.kind === "children").map((p) => [p.name, []]))])}>+ {prop.name.replace(/s$/, "")}</button>
    </div>
  );
}

function StepsEditor({ steps, ctx, onChange }: { steps: any[]; ctx: Ctx; onChange(v: any[]): void }) {
  const stepDef = (n: string): UiStep | undefined => ctx.cat.steps.find((s) => s.name === n);
  const set = (i: number, k: string, v: any) => onChange(steps.map((s, j) => {
    if (j !== i) return s;
    const n = { ...s };
    if (v === undefined || v === "") delete n[k];
    else n[k] = v;
    return n;
  }));
  return (
    <div className="ui-steps">
      {steps.map((s, i) => {
        const def = stepDef(s.do);
        return (
          <div key={i} className="ui-object">
            <div className="row" style={{ justifyContent: "space-between" }}>
              <select value={s.do} onChange={(e) => onChange(steps.map((x, j) => (j === i ? { do: e.target.value } : x)))} style={{ width: "auto" }}>
                {ctx.cat.steps.map((x) => <option key={x.name}>{x.name}</option>)}
              </select>
              <span className="row" style={{ gap: 2 }}>
                <button disabled={i === 0} onClick={() => { const n = [...steps]; [n[i - 1], n[i]] = [n[i], n[i - 1]]; onChange(n); }}>↑</button>
                <button onClick={() => onChange(steps.filter((_, j) => j !== i))}>✕</button>
              </span>
            </div>
            {(def?.props || []).map((p) => (
              <PropField key={p.name} prop={p} value={s[p.name]} ctx={{ ...ctx, type: typeof s.type === "string" ? s.type : ctx.type }} onChange={(v) => set(i, p.name, v)} />
            ))}
            {def?.api && (
              <details>
                <summary className="muted small">on error</summary>
                <StepsEditor steps={s.onError || []} ctx={ctx} onChange={(v) => set(i, "onError", v.length ? v : undefined)} />
              </details>
            )}
          </div>
        );
      })}
      <select value="" onChange={(e) => e.target.value && onChange([...steps, { do: e.target.value }])}>
        <option value="">+ step…</option>
        {ctx.cat.steps.map((x) => <option key={x.name} value={x.name}>{x.name} — {x.doc}</option>)}
      </select>
    </div>
  );
}

const SCALARS = ["string", "integer", "number", "boolean"];

function PageInspector({ ctx, update }: { ctx: Ctx; update(d: Doc): void }) {
  const d = ctx.doc;
  const set = (k: string, v: any) => update(setAt(d, `/${k}`, v === "" ? undefined : v));
  const types = Object.keys(ctx.src.types).sort();
  const params = Object.entries((d.params || {}) as Record<string, { type: string }>);
  const state = Object.entries((d.state || {}) as Record<string, { type: string; default?: unknown }>);
  const data = Object.entries((d.data || {}) as Record<string, any>);
  const kindOf = (s: any) => ["feature", "children", "search", "query"].find((k) => s[k] !== undefined) || "feature";
  const renameKey = (obj: Record<string, any>, from: string, to: string) => Object.fromEntries(Object.entries(obj).map(([k, v]) => [k === from ? to : k, v]));
  return (
    <div>
      <h2 style={{ marginTop: 0 }}>Page</h2>
      <label>page (name) *</label><input value={d.page} onChange={(e) => set("page", e.target.value)} />
      <label>route * <span className="muted small">/path/:param</span></label><input className="mono" value={d.route ?? ""} onChange={(e) => set("route", e.target.value)} />
      <label>title <span className="muted small">{hint.text}</span></label><input value={d.title ?? ""} onChange={(e) => set("title", e.target.value)} />
      <label>visible <span className="muted small">expression over principal</span></label><input className="mono" value={d.visible ?? ""} onChange={(e) => set("visible", e.target.value)} placeholder="'admin' in principal.roles" />

      <h2>Params</h2>
      {params.map(([k, p]) => (
        <div className="row" key={k} style={{ gap: 4, flexWrap: "nowrap" }}>
          <input className="mono" defaultValue={k} onBlur={(e) => e.target.value !== k && set("params", renameKey(d.params, k, e.target.value))} />
          <select value={p.type} onChange={(e) => set("params", { ...d.params, [k]: { ...p, type: e.target.value } })}>{SCALARS.map((x) => <option key={x}>{x}</option>)}</select>
          <button onClick={() => { const n = { ...d.params }; delete n[k]; set("params", Object.keys(n).length ? n : undefined); }}>✕</button>
        </div>
      ))}
      <button className="link" onClick={() => set("params", { ...(d.params || {}), [`p${params.length + 1}`]: { type: "string" } })}>+ param</button>

      <h2>State</h2>
      {state.map(([k, s]) => (
        <div className="row" key={k} style={{ gap: 4, flexWrap: "nowrap" }}>
          <input className="mono" defaultValue={k} onBlur={(e) => e.target.value !== k && set("state", renameKey(d.state, k, e.target.value))} />
          <select value={s.type} onChange={(e) => set("state", { ...d.state, [k]: { type: e.target.value } })}>{[...SCALARS, "array", "object"].map((x) => <option key={x}>{x}</option>)}</select>
          <button onClick={() => { const n = { ...d.state }; delete n[k]; set("state", Object.keys(n).length ? n : undefined); }}>✕</button>
        </div>
      ))}
      <button className="link" onClick={() => set("state", { ...(d.state || {}), [`s${state.length + 1}`]: { type: "string" } })}>+ state variable</button>

      <h2>Data</h2>
      {data.map(([k, s]) => {
        const kind = kindOf(s);
        const put = (v: any) => set("data", { ...d.data, [k]: v });
        return (
          <div key={k} className="ui-object">
            <div className="row" style={{ gap: 4, flexWrap: "nowrap" }}>
              <input className="mono" defaultValue={k} onBlur={(e) => e.target.value !== k && set("data", renameKey(d.data, k, e.target.value))} />
              <select value={kind} onChange={(e) => put(e.target.value === "query" ? { query: { type: s.type || types[0] } } : { [e.target.value]: s[kind] ?? "", type: s.type ?? s.query?.type })}>
                {["feature", "children", "query", "search"].map((x) => <option key={x}>{x}</option>)}
              </select>
              <button onClick={() => { const n = { ...d.data }; delete n[k]; set("data", Object.keys(n).length ? n : undefined); }}>✕</button>
            </div>
            {kind === "query" ? (
              <JsonField value={s.query} onChange={(q) => put({ query: q })} />
            ) : (
              <>
                <select value={s.type ?? ""} onChange={(e) => put({ ...s, type: e.target.value })}><option value=""></option>{types.map((x) => <option key={x}>{x}</option>)}</select>
                <input className="mono" value={s[kind] ?? ""} placeholder={kind === "search" ? "expression: words" : "expression: ID, e.g. params.id"} onChange={(e) => put({ ...s, [kind]: e.target.value })} />
              </>
            )}
          </div>
        );
      })}
      <button className="link" onClick={() => set("data", { ...(d.data || {}), [`source${data.length + 1}`]: { feature: "params.id", type: types.find((t) => !["user", "team", "grant", "root"].includes(t)) || types[0] } })}>+ data source</button>
    </div>
  );
}

function JsonField({ value, onChange }: { value: unknown; onChange(v: any): void }) {
  const [text, setText] = useState(JSON.stringify(value, null, 2));
  const [err, setErr] = useState("");
  return (
    <>
      <textarea className="mono" rows={6} value={text} onChange={(e) => {
        setText(e.target.value);
        try { onChange(JSON.parse(e.target.value)); setErr(""); } catch (x) { setErr(String(x)); }
      }} />
      {err && <div className="error small">{err}</div>}
    </>
  );
}
