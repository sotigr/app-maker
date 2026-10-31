import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Background, Controls, Edge, Handle, MiniMap, Node, NodeProps, Position, ReactFlow, ReactFlowProvider,
  addEdge, Connection, useReactFlow,
} from "@xyflow/react";
import { Api, FlowDefinition, FlowProblem, NodeInfo, NodeSpec, describeError } from "../api";

// The visual flow editor (ADR 0002). The flow JSON is the source of truth;
// the canvas is a view of it. Explicit edges are solid, dependencies
// inferred from `nodes.X` references are dashed (ADR 0007).

const TRIGGER = "__trigger";

function refsOf(v: any, out = new Set<string>()): Set<string> {
  if (typeof v === "string") {
    for (const m of v.matchAll(/\bnodes\.([A-Za-z_][A-Za-z0-9_]*)/g)) out.add(m[1]);
  } else if (v && typeof v === "object") {
    for (const x of Object.values(v)) refsOf(x, out);
  }
  return out;
}

function deps(def: FlowDefinition): Map<string, Set<string>> {
  const d = new Map<string, Set<string>>();
  for (const [name, n] of Object.entries(def.nodes)) {
    const s = refsOf([n.in, n.when]);
    s.delete(name);
    d.set(name, s);
  }
  for (const [a, b] of def.edges || []) d.get(b)?.add(a);
  for (const [name, n] of Object.entries(def.nodes)) if (n.onError) d.get(n.onError)?.add(name);
  return d;
}

// autoLayout places nodes left to right by dependency level.
function autoLayout(def: FlowDefinition): Record<string, { x: number; y: number }> {
  const d = deps(def);
  const level = new Map<string, number>();
  const visit = (n: string, stack: Set<string>): number => {
    if (level.has(n)) return level.get(n)!;
    if (stack.has(n)) return 0;
    stack.add(n);
    let l = 0;
    for (const p of d.get(n) || []) if (def.nodes[p]) l = Math.max(l, visit(p, stack) + 1);
    stack.delete(n);
    level.set(n, l);
    return l;
  };
  Object.keys(def.nodes).forEach((n) => visit(n, new Set()));
  const rows = new Map<number, number>();
  const out: Record<string, { x: number; y: number }> = {};
  for (const n of Object.keys(def.nodes).sort()) {
    const l = level.get(n) || 0;
    const r = rows.get(l) || 0;
    rows.set(l, r + 1);
    out[n] = { x: 300 + l * 320, y: 40 + r * 120 };
  }
  return out;
}

function triggerLabel(t: Record<string, any>): string {
  if (t.action) return `action ${t.action}`;
  if (t.beforeWrite) return `beforeWrite ${t.beforeWrite.join(",")}`;
  if (t.event) return `event ${t.event.join(",") || "any"}`;
  if (t.schedule) return `schedule ${t.schedule}`;
  if (t.webhook) return `webhook ${t.webhook}`;
  return "no trigger";
}

type NodeData = { label: string; type: string; kind: string; summary: string; bad: boolean };

function FlowNode({ data, selected }: NodeProps<Node<NodeData>>) {
  return (
    <div className={`fnode k-${data.kind} ${selected ? "selected" : ""} ${data.bad ? "bad" : ""}`}>
      <Handle type="target" position={Position.Left} />
      <div className="head">
        <span>{data.label}</span>
        <span className="muted">{data.type}</span>
      </div>
      <div className="body" title={data.summary}>{data.summary || " "}</div>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}

function TriggerNode({ data }: NodeProps<Node<NodeData>>) {
  return (
    <div className="fnode k-Control">
      <div className="head"><span>trigger</span></div>
      <div className="body">{data.summary}</div>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}

const nodeTypes = { fnode: FlowNode, trigger: TriggerNode };

const NEW_FLOW: FlowDefinition = {
  flow: "newFlow",
  title: "New flow",
  trigger: { event: ["created"], on: "" },
  nodes: {},
  edges: [],
};

export function FlowEditor({ name }: { name: string }) {
  return (
    <ReactFlowProvider>
      <Editor name={name} />
    </ReactFlowProvider>
  );
}

function Editor({ name }: { name: string }) {
  const [catalog, setCatalog] = useState<NodeInfo[]>([]);
  const [def, setDef] = useState<FlowDefinition>();
  const [selected, setSelected] = useState<string>();
  const [problems, setProblems] = useState<FlowProblem[]>([]);
  const [status, setStatus] = useState("");
  const [showJSON, setShowJSON] = useState(false);
  const [dirty, setDirty] = useState(false);
  const rf = useReactFlow();

  useEffect(() => {
    Api.flows().then((r) => {
      setCatalog(r.nodes);
      if (name === "new") {
        setDef(structuredClone(NEW_FLOW));
        return;
      }
      const f = r.flows.find((x) => x.name === name);
      if (!f) {
        setStatus(`unknown flow ${name}`);
        return;
      }
      const d = structuredClone(f.definition);
      if (!d["x-layout"]) d["x-layout"] = autoLayout(d);
      setDef(d);
    }, (e) => setStatus(describeError(e)));
  }, [name]);

  const kinds = useMemo(() => Object.fromEntries(catalog.map((c) => [c.name, c])), [catalog]);
  const badNodes = useMemo(() => {
    const s = new Set<string>();
    for (const p of problems) {
      const m = p.pointer.match(/^\/nodes\/([^/]+)/);
      if (m) s.add(m[1]);
    }
    return s;
  }, [problems]);

  const update = useCallback((f: (d: FlowDefinition) => void) => {
    setDef((old) => {
      if (!old) return old;
      const d = structuredClone(old);
      f(d);
      return d;
    });
    setDirty(true);
  }, []);

  const { nodes, edges } = useMemo(() => {
    if (!def) return { nodes: [], edges: [] };
    const layout = def["x-layout"] || {};
    const nodes: Node<NodeData>[] = [
      { id: TRIGGER, type: "trigger", position: layout[TRIGGER] || { x: 0, y: 40 }, data: { label: "trigger", type: "", kind: "Control", summary: triggerLabel(def.trigger) + (def.trigger.on ? ` on ${def.trigger.on.split(":").pop()}` : ""), bad: problems.some((p) => p.pointer.startsWith("/trigger")) }, deletable: false },
    ];
    for (const [n, spec] of Object.entries(def.nodes)) {
      const summary = Object.entries(spec.in || {}).map(([k, v]) => `${k}: ${typeof v === "string" ? v : JSON.stringify(v)}`).join(", ");
      nodes.push({
        id: n, type: "fnode", position: layout[n] || { x: 260, y: 40 }, selected: n === selected,
        data: { label: n, type: spec.node, kind: kinds[spec.node]?.kind || "Pure", summary: (spec.when ? `when ${spec.when} · ` : "") + summary, bad: badNodes.has(n) },
      });
    }
    const edges: Edge[] = [];
    const explicit = new Set((def.edges || []).map(([a, b]) => `${a}->${b}`));
    for (const [a, b] of def.edges || []) edges.push({ id: `e:${a}->${b}`, source: a, target: b });
    const d = deps(def);
    for (const [b, srcs] of d) {
      if (srcs.size === 0) edges.push({ id: `t:${b}`, source: TRIGGER, target: b, style: { strokeDasharray: "2 4", opacity: 0.5 }, deletable: false, selectable: false });
      for (const a of srcs) {
        if (!def.nodes[a] || explicit.has(`${a}->${b}`)) continue;
        const onErr = def.nodes[a].onError === b;
        edges.push({ id: `i:${a}->${b}`, source: a, target: b, animated: onErr, label: onErr ? "onError" : undefined,
          style: { strokeDasharray: "6 4" }, deletable: false, selectable: false });
      }
    }
    return { nodes, edges };
  }, [def, selected, kinds, badNodes, problems]);

  if (!def) return <p className="error">{status || "Loading…"}</p>;

  const addNode = (type: string, pos?: { x: number; y: number }) => {
    const base = type.split(".").pop()!.replace(/[^A-Za-z0-9]/g, "") || "node";
    let i = 1;
    while (def.nodes[`${base}${i}`]) i++;
    const n = `${base}${i}`;
    const spec: NodeSpec = { node: type, in: {} };
    for (const p of kinds[type]?.inputs || []) if (p.required) spec.in![p.name] = p.map ? {} : "";
    if (type === "forEach") spec.do = { nodes: { body: { node: "expr", in: { expr: "item" } } }, result: "nodes.body.out" };
    update((d) => {
      d.nodes[n] = spec;
      d["x-layout"] = { ...(d["x-layout"] || {}), [n]: pos || { x: 300 + Math.random() * 200, y: 60 + Math.random() * 200 } };
    });
    setSelected(n);
  };

  const validate = async () => {
    setStatus("validating…");
    try {
      const r = await Api.validateFlow(def);
      setProblems(r.problems);
      setStatus(r.valid ? "valid ✓" : `${r.problems.length} problem(s)`);
    } catch (e) {
      setStatus(describeError(e));
    }
  };
  const save = async () => {
    setStatus("saving…");
    try {
      const r = await Api.saveFlow(def);
      setProblems([]);
      setDirty(false);
      setStatus(`saved to ${r.saved}; fw dev reloads it`);
      if (name === "new") location.hash = `#/flows/${encodeURIComponent(def.flow)}`;
    } catch (e: any) {
      const probs = e?.problem?.problems;
      if (probs) {
        setProblems(probs);
        setStatus(`not saved: ${probs.length} problem(s)`);
      } else setStatus(`not saved: ${describeError(e)}${e?.problem?.code === "not_found" ? " (saving needs fw dev)" : ""}`);
    }
  };

  const groups = new Map<string, NodeInfo[]>();
  for (const c of catalog) groups.set(c.kind, [...(groups.get(c.kind) || []), c]);

  return (
    <>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}><a href="#/flows">Flows</a> / {def.flow}{dirty ? " •" : ""}</h1>
        <button onClick={validate}>Validate</button>
        <button className="primary" onClick={save}>Save</button>
        <button onClick={() => update((d) => { d["x-layout"] = autoLayout(d); })}>Auto layout</button>
        <button onClick={() => setShowJSON(!showJSON)}>{showJSON ? "Canvas" : "JSON"}</button>
        <span className="muted">{status}</span>
      </div>
      {problems.length > 0 && (
        <div className="card problems">
          {problems.map((p, i) => (
            <div key={i} className="error mono" onClick={() => { const m = p.pointer.match(/^\/nodes\/([^/]+)/); if (m) setSelected(m[1]); }}>
              {p.pointer || "/"}: {p.message}
            </div>
          ))}
        </div>
      )}
      {showJSON ? (
        <JSONEditor def={def} onApply={(d) => { setDef(d); setDirty(true); setShowJSON(false); }} />
      ) : (
        <div className="editor">
          <div className="palette">
            <div className="muted">Drag or click to add</div>
            {[...groups.entries()].map(([kind, list]) => (
              <div key={kind}>
                <div className="kind">{kind}</div>
                {list.map((c) => (
                  <div key={c.name} className={`item k-${kind}`} title={c.description} draggable
                    onDragStart={(e) => e.dataTransfer.setData("application/fw-node", c.name)} onClick={() => addNode(c.name)}>
                    {c.name}
                  </div>
                ))}
              </div>
            ))}
          </div>
          <div className="canvas" onDragOver={(e) => e.preventDefault()} onDrop={(e) => {
            const t = e.dataTransfer.getData("application/fw-node");
            if (t) addNode(t, rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }));
          }}>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              fitView
              onNodeClick={(_, n) => setSelected(n.id === TRIGGER ? undefined : n.id)}
              onPaneClick={() => setSelected(undefined)}
              onNodeDragStop={(_, n) => update((d) => { d["x-layout"] = { ...(d["x-layout"] || {}), [n.id]: n.position }; })}
              onConnect={(c: Connection) => {
                if (!c.source || !c.target || c.source === TRIGGER || c.source === c.target) return;
                update((d) => {
                  const list = d.edges || [];
                  if (!list.some(([a, b]) => a === c.source && b === c.target)) d.edges = [...list, [c.source!, c.target!]];
                });
                void addEdge;
              }}
              onEdgesDelete={(es) => update((d) => {
                const gone = new Set(es.map((e) => e.id));
                d.edges = (d.edges || []).filter(([a, b]) => !gone.has(`e:${a}->${b}`));
              })}
              onNodesDelete={(ns) => update((d) => {
                for (const n of ns) {
                  delete d.nodes[n.id];
                  if (d["x-layout"]) delete d["x-layout"][n.id];
                }
                const gone = new Set(ns.map((n) => n.id));
                d.edges = (d.edges || []).filter(([a, b]) => !gone.has(a) && !gone.has(b));
                setSelected(undefined);
              })}
              deleteKeyCode={["Delete", "Backspace"]}
            >
              <Background />
              <Controls />
              <MiniMap pannable zoomable />
            </ReactFlow>
          </div>
          <div className="inspector">
            {selected && def.nodes[selected] ? (
              <NodeInspector key={selected} name={selected} def={def} catalog={kinds} update={update}
                rename={(to) => { update((d) => renameNode(d, selected, to)); setSelected(to); }} />
            ) : (
              <FlowInspector def={def} update={update} />
            )}
          </div>
        </div>
      )}
    </>
  );
}

function renameNode(d: FlowDefinition, from: string, to: string) {
  if (!to || d.nodes[to]) return;
  d.nodes[to] = d.nodes[from];
  delete d.nodes[from];
  const re = new RegExp(`\\bnodes\\.${from}\\b`, "g");
  const fix = (v: any): any => typeof v === "string" ? v.replace(re, `nodes.${to}`) : Array.isArray(v) ? v.map(fix) : v && typeof v === "object" ? Object.fromEntries(Object.entries(v).map(([k, x]) => [k, fix(x)])) : v;
  for (const n of Object.values(d.nodes)) {
    n.in = fix(n.in);
    if (n.when) n.when = fix(n.when);
    if (n.onError === from) n.onError = to;
  }
  d.edges = (d.edges || []).map(([a, b]) => [a === from ? to : a, b === from ? to : b]);
  if (d.result) d.result = fix(d.result);
  if (d["x-layout"]?.[from]) {
    d["x-layout"][to] = d["x-layout"][from];
    delete d["x-layout"][from];
  }
}

function NodeInspector({ name, def, catalog, update, rename }: {
  name: string; def: FlowDefinition; catalog: Record<string, NodeInfo>;
  update: (f: (d: FlowDefinition) => void) => void; rename: (to: string) => void;
}) {
  const spec = def.nodes[name];
  const info = catalog[spec.node];
  const [newName, setNewName] = useState(name);
  const set = (f: (n: NodeSpec) => void) => update((d) => f(d.nodes[name]));
  const known = new Set((info?.inputs || []).map((p) => p.name));
  const extra = Object.keys(spec.in || {}).filter((k) => !known.has(k));
  return (
    <>
      <h2>Node</h2>
      <label>Name</label>
      <div className="row">
        <input value={newName} onChange={(e) => setNewName(e.target.value)} />
        <button disabled={newName === name} onClick={() => rename(newName)}>Rename</button>
      </div>
      <label>Type</label>
      <select value={spec.node} onChange={(e) => set((n) => { n.node = e.target.value; })}>
        {Object.keys(catalog).sort().map((t) => <option key={t}>{t}</option>)}
      </select>
      <p className="muted">{info?.kind} · {info?.description} · out: {info?.output}</p>
      <h2>Inputs <span className="muted">(expressions)</span></h2>
      {(info?.inputs || []).map((p) => (
        <div key={p.name}>
          <label title={p.doc}>{p.name}{p.required ? " *" : ""} <span className="muted">{p.map ? "map" : p.type}</span></label>
          {p.map ? (
            <MapEditor value={spec.in?.[p.name] || {}} onChange={(v) => set((n) => { n.in = { ...(n.in || {}) }; if (Object.keys(v).length) n.in[p.name] = v; else delete n.in[p.name]; })} />
          ) : (
            <input className="mono" value={typeof spec.in?.[p.name] === "string" ? spec.in[p.name] : spec.in?.[p.name] === undefined ? "" : JSON.stringify(spec.in[p.name])}
              placeholder={p.doc}
              onChange={(e) => set((n) => { n.in = { ...(n.in || {}) }; if (e.target.value === "" && !p.required) delete n.in[p.name]; else n.in[p.name] = e.target.value; })} />
          )}
        </div>
      ))}
      {extra.length > 0 && <p className="error">Unknown inputs: {extra.join(", ")}</p>}
      <label>when <span className="muted">(skip unless true)</span></label>
      <input className="mono" value={spec.when || ""} onChange={(e) => set((n) => { if (e.target.value) n.when = e.target.value; else delete n.when; })} />
      <label>onError</label>
      <select value={spec.onError || ""} onChange={(e) => set((n) => { if (e.target.value) n.onError = e.target.value; else delete n.onError; })}>
        <option value="">— fail the run —</option>
        {Object.keys(def.nodes).filter((x) => x !== name).map((x) => <option key={x}>{x}</option>)}
      </select>
      {spec.node === "forEach" && (
        <>
          <label>maxItems</label>
          <input type="number" value={spec.maxItems ?? 100} onChange={(e) => set((n) => { n.maxItems = Number(e.target.value); })} />
          <label>do (subgraph JSON)</label>
          <JSONField value={spec.do} onChange={(v) => set((n) => { n.do = v; })} />
        </>
      )}
      <p className="muted">Reference outputs as <span className="mono">nodes.{name}.out</span>.</p>
    </>
  );
}

function MapEditor({ value, onChange }: { value: Record<string, any>; onChange: (v: Record<string, any>) => void }) {
  const entries = Object.entries(value);
  return (
    <div>
      {entries.map(([k, v], i) => (
        <div className="row" key={i} style={{ flexWrap: "nowrap", marginBottom: 4 }}>
          <input className="mono" style={{ width: "40%" }} value={k} onChange={(e) => {
            const out: Record<string, any> = {};
            entries.forEach(([kk, vv], j) => { out[j === i ? e.target.value : kk] = vv; });
            onChange(out);
          }} />
          <input className="mono" value={typeof v === "string" ? v : JSON.stringify(v)} onChange={(e) => onChange({ ...value, [k]: e.target.value })} />
          <button onClick={() => { const out = { ...value }; delete out[k]; onChange(out); }}>×</button>
        </div>
      ))}
      <button onClick={() => { let i = 1; while (`key${i}` in value) i++; onChange({ ...value, [`key${i}`]: "" }); }}>+ entry</button>
    </div>
  );
}

function JSONField({ value, onChange }: { value: any; onChange: (v: any) => void }) {
  const [text, setText] = useState(() => (value === undefined ? "" : JSON.stringify(value, null, 2)));
  const [err, setErr] = useState("");
  return (
    <>
      <textarea style={{ minHeight: 120 }} value={text} onChange={(e) => {
        setText(e.target.value);
        try {
          onChange(e.target.value.trim() ? JSON.parse(e.target.value) : undefined);
          setErr("");
        } catch (x) {
          setErr(String(x));
        }
      }} />
      {err && <div className="error">{err}</div>}
    </>
  );
}

function FlowInspector({ def, update }: { def: FlowDefinition; update: (f: (d: FlowDefinition) => void) => void }) {
  const field = (k: keyof FlowDefinition, label: string) => (
    <>
      <label>{label}</label>
      <input value={(def[k] as any) ?? ""} onChange={(e) => update((d) => { (d as any)[k] = e.target.value || undefined; })} />
    </>
  );
  return (
    <>
      <h2>Flow</h2>
      {field("flow", "Name")}
      {field("title", "Title")}
      <label>Trigger</label>
      <JSONField value={def.trigger} onChange={(v) => update((d) => { d.trigger = v || {}; })} />
      <p className="muted mono">{`{"action":"x","on":"urn:app:feature:t"} · {"event":["updated"],"on":…,"when":"changed('/f')"} · {"beforeWrite":["create"],"on":…} · {"schedule":"0 8 * * *"} · {"webhook":"name","secret":"\${secret:n}"}`}</p>
      <div className="row">
        <label><input type="checkbox" checked={def.runAs === "system"} onChange={(e) => update((d) => { d.runAs = e.target.checked ? "system" : undefined; })} /> run as system</label>
        <label><input type="checkbox" checked={!!def.privileged} onChange={(e) => update((d) => { d.privileged = e.target.checked || undefined; })} /> privileged</label>
        <label><input type="checkbox" checked={!!def.trace} onChange={(e) => update((d) => { d.trace = e.target.checked || undefined; })} /> trace</label>
      </div>
      <label>timeoutMs</label>
      <input type="number" value={def.timeoutMs ?? ""} onChange={(e) => update((d) => { d.timeoutMs = e.target.value ? Number(e.target.value) : undefined; })} />
      <label>Input schema</label>
      <JSONField value={def.input} onChange={(v) => update((d) => { d.input = v; })} />
      <label>Output schema</label>
      <JSONField value={def.output} onChange={(v) => update((d) => { d.output = v; })} />
      <label>Result (key → expression)</label>
      <JSONField value={def.result} onChange={(v) => update((d) => { d.result = v; })} />
    </>
  );
}

function JSONEditor({ def, onApply }: { def: FlowDefinition; onApply: (d: FlowDefinition) => void }) {
  const [text, setText] = useState(JSON.stringify(def, null, 2));
  const [err, setErr] = useState("");
  return (
    <div className="card">
      <textarea style={{ minHeight: "60vh" }} value={text} onChange={(e) => setText(e.target.value)} />
      <p className="error">{err}</p>
      <button className="primary" onClick={() => { try { onApply(JSON.parse(text)); } catch (x) { setErr(String(x)); } }}>Apply JSON</button>
    </div>
  );
}
