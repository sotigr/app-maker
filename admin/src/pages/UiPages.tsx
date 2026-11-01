// UI builder home (ADR 0016): the pages of ui/, the app shell (ui/app.json),
// the app's styles (ui/styles.css), and assets (ui/assets/).
import { useEffect, useState } from "react";
import { Api, ApiError, describeError, type UiProblem, type UiSource } from "../api";

function Problems({ problems }: { problems: UiProblem[] }) {
  if (!problems.length) return null;
  return (
    <div className="card">
      {problems.map((p, i) => <div key={i} className={p.warning ? "muted" : "error"}><span className="mono">{p.file}#{p.pointer}</span> {p.message}</div>)}
    </div>
  );
}

function useSource(): [UiSource | undefined, string, () => void] {
  const [src, setSrc] = useState<UiSource>();
  const [err, setErr] = useState("");
  const load = () => Api.uiSource().then(setSrc, (e) => setErr(describeError(e)));
  useEffect(() => { load(); }, []);
  return [src, err, load];
}

export function UiPages() {
  const [src, err] = useSource();
  const [name, setName] = useState("");
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>UI</h1>
        <div className="row">
          <a href="#/ui/app"><button>App shell</button></a>
          <a href="#/ui/styles"><button>Styles &amp; assets</button></a>
          <input placeholder="newPage" value={name} onChange={(e) => setName(e.target.value)} style={{ width: 160 }} />
          <a href={`#/ui/pages/${encodeURIComponent(name || "new")}`}><button className="primary">New page</button></a>
        </div>
      </div>
      <p className="error">{err}</p>
      {src && !src.app && <p className="muted">This app has no end-user UI yet. Add a page, then set its home in the app shell. Served at <span className="mono">/</span>.</p>}
      <table>
        <thead><tr><th>Page</th><th>Route</th><th>Visible</th><th>File</th></tr></thead>
        <tbody>
          {src?.pages.map((p) => (
            <tr key={p.name}>
              <td><a href={`#/ui/pages/${encodeURIComponent(p.name)}`}>{p.doc.title || p.name}</a> <span className="muted">{p.name}</span>{src.app?.home === p.name && <span className="pill"> home</span>}</td>
              <td className="mono"><a href={p.doc.route} target="_blank" rel="noreferrer">{p.doc.route}</a></td>
              <td className="mono">{p.doc.visible || "everyone"}</td>
              <td className="mono">{p.file}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="muted">Pages are JSON files in <span className="mono">ui/pages/</span>; the builder writes them under <span className="mono">organon dev</span>. Agents and people can edit the same files: run <span className="mono">organon ui catalog</span> for the components and <span className="mono">organon validate</span> to check them.</p>
    </>
  );
}

export function UiApp() {
  const [src, err, load] = useSource();
  const [doc, setDoc] = useState<any>();
  const [problems, setProblems] = useState<UiProblem[]>([]);
  const [status, setStatus] = useState("");
  useEffect(() => {
    if (src) setDoc(src.app || { title: "", home: src.pages[0]?.name || "", nav: [] });
  }, [src]);
  useEffect(() => {
    if (!doc) return;
    const t = setTimeout(() => Api.validateUi({ "ui/app.json": JSON.stringify(doc) }).then((r) => setProblems(r.problems.filter((p) => p.file === "ui/app.json")), () => undefined), 400);
    return () => clearTimeout(t);
  }, [doc]);
  if (!src || !doc) return <p className="error">{err}</p>;
  const set = (k: string, v: any) => setDoc({ ...doc, [k]: v === "" ? undefined : v });
  const nav: any[] = doc.nav || [];
  const setNav = (i: number, k: string, v: any) => set("nav", nav.map((n, j) => (j === i ? { ...n, [k]: v === "" ? undefined : v } : n)));
  const save = () => Api.saveUiApp(doc).then(() => { setStatus("Saved ui/app.json."); load(); }, (e) => {
    if (e instanceof ApiError && e.problem.problems) setProblems(e.problem.problems as UiProblem[]);
    setStatus(`Not saved: ${describeError(e)}`);
  });
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}><a href="#/ui">UI</a> / app shell</h1>
        <button className="primary" disabled={!src.dev} onClick={save}>Save</button>
      </div>
      <p className={status.startsWith("Not") ? "error" : "okmsg"}>{status}</p>
      <div className="card" style={{ maxWidth: 720 }}>
        <label>title</label><input value={doc.title ?? ""} onChange={(e) => set("title", e.target.value)} />
        <label>home page *</label>
        <select value={doc.home ?? ""} onChange={(e) => set("home", e.target.value)}>{src.pages.map((p) => <option key={p.name}>{p.name}</option>)}</select>
        <label>logo</label>
        <select value={doc.logo ?? ""} onChange={(e) => set("logo", e.target.value)}><option value=""></option>{src.assets.map((a) => <option key={a} value={`assets/${a}`}>{a}</option>)}</select>
        <label className="inline"><input type="checkbox" checked={doc.requireLogin !== false} onChange={(e) => set("requireLogin", e.target.checked ? undefined : false)} /> require login (otherwise pages visible to anonymous callers are public)</label>
        <label>not found text</label><input value={doc.notFound ?? ""} onChange={(e) => set("notFound", e.target.value)} />
        <h2>Navigation</h2>
        {nav.map((n, i) => (
          <div key={i} className="row" style={{ gap: 4, flexWrap: "nowrap", marginBottom: 4 }}>
            <input placeholder="label" value={n.label ?? ""} onChange={(e) => setNav(i, "label", e.target.value)} />
            <select value={n.page ?? ""} onChange={(e) => setNav(i, "page", e.target.value)}>{src.pages.map((p) => <option key={p.name}>{p.name}</option>)}</select>
            <input className="mono" placeholder="visible (expression)" value={n.visible ?? ""} onChange={(e) => setNav(i, "visible", e.target.value)} />
            <button onClick={() => set("nav", nav.filter((_, j) => j !== i))}>✕</button>
          </div>
        ))}
        <button className="link" onClick={() => set("nav", [...nav, { label: "", page: src.pages[0]?.name }])}>+ nav entry</button>
      </div>
      <Problems problems={problems} />
    </>
  );
}

export function UiStyles() {
  const [src, err, load] = useSource();
  const [css, setCss] = useState<string>();
  const [status, setStatus] = useState("");
  const [problems, setProblems] = useState<UiProblem[]>([]);
  const [path, setPath] = useState("");
  useEffect(() => { if (src && css === undefined) setCss(src.styles || ""); }, [src]);
  if (!src || css === undefined) return <p className="error">{err}</p>;
  const fail = (e: unknown) => {
    if (e instanceof ApiError && e.problem.problems) setProblems(e.problem.problems as UiProblem[]);
    setStatus(`Not saved: ${describeError(e)}`);
  };
  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}><a href="#/ui">UI</a> / styles &amp; assets</h1>
        <button className="primary" disabled={!src.dev} onClick={() => Api.saveStyles(css).then(() => { setStatus("Saved ui/styles.css."); load(); }, fail)}>Save styles</button>
      </div>
      <p className={status.startsWith("Not") ? "error" : "okmsg"}>{status}</p>
      <p className="muted">Loaded after the base theme. Override variables such as <span className="mono">--o-accent</span>, <span className="mono">--o-radius</span>, <span className="mono">--o-font</span>, or style <span className="mono">o-*</span> classes and the <span className="mono">class</span> you give components.</p>
      <textarea className="ui-css" value={css} onChange={(e) => setCss(e.target.value)} spellCheck={false} />
      <h2>Assets</h2>
      <table>
        <tbody>
          {src.assets.map((a) => (
            <tr key={a}>
              <td className="mono">assets/{a}</td>
              <td>{/\.(png|jpe?g|gif|webp|avif|svg|ico)$/i.test(a) && <img src={`/assets/app/${a}`} alt="" style={{ height: 32 }} />}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="row" style={{ marginTop: 8 }}>
        <input placeholder="path, e.g. logo.svg" value={path} onChange={(e) => setPath(e.target.value)} style={{ width: 220 }} />
        <input type="file" disabled={!src.dev} onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) Api.saveAsset(path || f.name, f).then(() => { setStatus(`Saved assets/${path || f.name}.`); load(); }, fail);
        }} style={{ width: "auto" }} />
      </div>
      <Problems problems={problems} />
    </>
  );
}
