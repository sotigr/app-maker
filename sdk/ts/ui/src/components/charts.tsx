// Board and charts. Charts are plain SVG over the rows a source has loaded
// (at most 500); exact totals belong in x-aggregate fields.
import { useState } from "react";
import { Node, register, WithScope, type ComponentProps } from "../render";
import { formatNumber, typeMeta, useEval, usePage, useRuntime, withClass } from "../runtime";
import { describe } from "../steps";
import type { NodeDoc, Row } from "../types";

function field(row: Row, path: string): unknown {
  return row[path.slice(1).split("/")[0]];
}

function sourceType(rt: ReturnType<typeof useRuntime>, page: string, source: unknown): string | undefined {
  const s = rt.bundle.pages.find((p) => p.name === page)?.doc.data?.[source as string];
  return s?.type ?? s?.query?.type;
}

register("board", ({ node, path }: ComponentProps) => {
  const rt = useRuntime();
  const ev = useEval();
  const page = usePage();
  const [over, setOver] = useState<string>();
  const rows = ((ev.env.data as Record<string, unknown>)?.[node.source as string] as Row[] | null) ?? null;
  const type = sourceType(rt, page.name, node.source);
  const meta = typeMeta(rt, type);
  const f = node.field as string;
  const sm = meta?.stateMachines?.[f];
  const states = meta?.fields[f]?.enum?.filter((x) => x != null) || [];
  const className = withClass("o-board", node);
  if (!rows) return <div className="o-loading" aria-busy="true" />;
  const drop = async (id: string, to: string) => {
    setOver(undefined);
    const row = rows.find((r) => r.id === id);
    const from = row && field(row, f);
    if (!row || from === to) return;
    if (!sm?.transitions.some((t) => t.from === from && t.to === to)) {
      rt.toast(`Cannot move from ${String(from).replace(/_/g, " ")} to ${to.replace(/_/g, " ")}.`, "warning");
      return;
    }
    try {
      await rt.client.action(row.type, row.id, "transition", { to });
    } catch (e) {
      rt.toast(describe(e), "danger");
    }
  };
  return (
    <div className={className}>
      {states.map((st) => {
        const items = rows.filter((r) => field(r, f) === st);
        return (
          <div key={st} className={`o-board-col${over === st ? " o-board-over" : ""}`}
            onDragOver={(e) => { e.preventDefault(); setOver(st); }}
            onDragLeave={() => setOver(undefined)}
            onDrop={(e) => { e.preventDefault(); drop(e.dataTransfer.getData("text/x-organon-id"), st); }}>
            <div className="o-board-head">
              <span>{st.replace(/_/g, " ")}</span>
              <span className="o-muted">{items.length}</span>
            </div>
            {items.map((item, index) => (
              <div key={item.id} className="o-board-card" draggable={!rt.editor}
                onDragStart={(e) => e.dataTransfer.setData("text/x-organon-id", item.id)}>
                <WithScope vars={{ item, index }}>
                  <Node node={node.card as NodeDoc} path={`${path}/card`} />
                </WithScope>
              </div>
            ))}
          </div>
        );
      })}
    </div>
  );
});

interface Point {
  label: string;
  value: number;
}

function points(node: NodeDoc, ev: ReturnType<typeof useEval>): Point[] | null {
  if (Array.isArray(node.series)) {
    return (node.series as { label: string; value: unknown }[]).map((s) => ({ label: ev.text(s.label), value: Number(ev.ex(s.value)) || 0 }));
  }
  const rows = ((ev.env.data as Record<string, unknown>)?.[node.source as string] as Row[] | null) ?? null;
  if (!rows) return null;
  const groups = new Map<string, number[]>();
  for (const r of rows) {
    const k = String(field(r, node.groupBy as string) ?? "—");
    const v = node.value ? Number(field(r, node.value as string)) || 0 : 1;
    groups.set(k, [...(groups.get(k) || []), v]);
  }
  const op = (node.op as string) || "count";
  return [...groups.entries()].map(([label, vs]) => ({
    label: label.replace(/_/g, " "),
    value: op === "count" && !node.value ? vs.length : op === "avg" ? vs.reduce((a, b) => a + b, 0) / vs.length : vs.reduce((a, b) => a + b, 0),
  }));
}

const W = 320;

function Bars({ ps, h }: { ps: Point[]; h: number }) {
  const max = Math.max(1, ...ps.map((p) => p.value));
  const bw = W / Math.max(ps.length, 1);
  return (
    <svg viewBox={`0 0 ${W} ${h + 20}`} className="o-chart-svg" role="img">
      {ps.map((p, i) => {
        const bh = (p.value / max) * (h - 14);
        return (
          <g key={i}>
            <rect x={i * bw + bw * 0.15} y={h - bh} width={bw * 0.7} height={bh} className={`o-chart-c${i % 6}`} rx={3} />
            <text x={i * bw + bw / 2} y={h - bh - 3} textAnchor="middle" className="o-chart-value">{formatNumber(p.value, Number.isInteger(p.value) ? 0 : 1)}</text>
            <text x={i * bw + bw / 2} y={h + 14} textAnchor="middle" className="o-chart-label">{p.label}</text>
          </g>
        );
      })}
    </svg>
  );
}

function Line({ ps, h }: { ps: Point[]; h: number }) {
  const max = Math.max(1, ...ps.map((p) => p.value));
  const step = W / Math.max(ps.length - 1, 1);
  const xy = ps.map((p, i) => [ps.length === 1 ? W / 2 : i * step, h - (p.value / max) * (h - 14)] as const);
  return (
    <svg viewBox={`-6 0 ${W + 12} ${h + 20}`} className="o-chart-svg" role="img">
      <polyline points={xy.map(([x, y]) => `${x},${y}`).join(" ")} className="o-chart-line" fill="none" />
      {xy.map(([x, y], i) => (
        <g key={i}>
          <circle cx={x} cy={y} r={3} className="o-chart-c0" />
          <text x={x} y={h + 14} textAnchor="middle" className="o-chart-label">{ps[i].label}</text>
        </g>
      ))}
    </svg>
  );
}

function Pie({ ps, h }: { ps: Point[]; h: number }) {
  const total = ps.reduce((a, p) => a + p.value, 0) || 1;
  const r = h / 2 - 2;
  let a0 = -Math.PI / 2;
  return (
    <div className="o-pie">
      <svg viewBox={`0 0 ${h} ${h}`} width={h} height={h} role="img">
        {ps.map((p, i) => {
          const a1 = a0 + (p.value / total) * Math.PI * 2;
          const large = a1 - a0 > Math.PI ? 1 : 0;
          const [x0, y0, x1, y1] = [h / 2 + r * Math.cos(a0), h / 2 + r * Math.sin(a0), h / 2 + r * Math.cos(a1), h / 2 + r * Math.sin(a1)];
          const d = ps.length === 1 ? `M ${h / 2} ${h / 2 - r} A ${r} ${r} 0 1 1 ${h / 2 - 0.01} ${h / 2 - r} Z`
            : `M ${h / 2} ${h / 2} L ${x0} ${y0} A ${r} ${r} 0 ${large} 1 ${x1} ${y1} Z`;
          a0 = a1;
          return <path key={i} d={d} className={`o-chart-c${i % 6}`} />;
        })}
      </svg>
      <ul className="o-legend">
        {ps.map((p, i) => (
          <li key={i}><span className={`o-swatch o-chart-c${i % 6}`} />{p.label} <span className="o-muted">{formatNumber(p.value)}</span></li>
        ))}
      </ul>
    </div>
  );
}

register("chart", ({ node }: ComponentProps) => {
  const ev = useEval();
  const ps = points(node, ev);
  const h = Number(node.height) || 160;
  const title = ev.text(node.title);
  const className = withClass("o-chart", node);
  return (
    <figure className={className}>
      {title && <figcaption>{title}</figcaption>}
      {!ps ? <div className="o-loading" aria-busy="true" /> : !ps.length ? <div className="o-empty">No data.</div>
        : node.kind === "pie" ? <Pie ps={ps} h={h} /> : node.kind === "line" ? <Line ps={ps} h={h} /> : <Bars ps={ps} h={h} />}
    </figure>
  );
});
