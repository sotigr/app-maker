// Layout and text components.
import { useState, type ReactNode } from "react";
import { Children, register, type ComponentProps } from "../render";
import { withClass, useEval, usePage, useRuntime } from "../runtime";

function cls(...parts: (string | false | undefined)[]) {
  return parts.filter(Boolean).join(" ");
}

register("stack", ({ node, path }: ComponentProps) => (
  <div className={withClass(cls("o-stack", `o-gap-${node.gap ?? "md"}`, !!node.align && `o-align-${node.align}`), node)}>
    <Children nodes={node.children} path={`${path}/children`} />
  </div>
));

register("row", ({ node, path }: ComponentProps) => (
  <div className={withClass(cls("o-row", `o-gap-${node.gap ?? "md"}`, !!node.align && `o-align-${node.align}`,
    !!node.justify && `o-justify-${node.justify}`), node)}>
    <Children nodes={node.children} path={`${path}/children`} />
  </div>
));

register("columns", ({ node, path }: ComponentProps) => (
  <div className={withClass("o-columns", node)}
    style={{ gridTemplateColumns: `repeat(auto-fit, minmax(min(${Number(node.min) || 240}px, 100%), 1fr))` }}>
    <Children nodes={node.children} path={`${path}/children`} />
  </div>
));

register("card", ({ node, path }: ComponentProps) => {
  const ev = useEval();
  const title = ev.text(node.title);
  return (
    <section className={withClass("o-card", node)}>
      {title && <h3 className="o-card-title">{title}</h3>}
      <Children nodes={node.children} path={`${path}/children`} />
    </section>
  );
});

register("tabs", ({ node, path }: ComponentProps) => {
  const ev = useEval();
  const page = usePage();
  const [local, setLocal] = useState(0);
  const key = node.state as string | undefined;
  const sel = key ? Number(page.state[key] ?? 0) : local;
  const set = (i: number) => (key ? page.setState(key, i) : setLocal(i));
  const tabs = (node.tabs as { label: string; children?: unknown }[]) || [];
  return (
    <div className={withClass("o-tabs", node)}>
      <div className="o-tab-list" role="tablist">
        {tabs.map((t, i) => (
          <button key={i} role="tab" aria-selected={i === sel} className={cls("o-tab", i === sel && "o-tab-active")} onClick={() => set(i)}>
            {ev.text(t.label)}
          </button>
        ))}
      </div>
      {tabs[sel] && (
        <div className="o-tab-panel" role="tabpanel">
          <Children nodes={tabs[sel].children} path={`${path}/tabs/${sel}/children`} />
        </div>
      )}
    </div>
  );
});

register("dialog", ({ node, path }: ComponentProps) => {
  const ev = useEval();
  const page = usePage();
  const rt = useRuntime();
  const name = node.name as string;
  const body = (
    <>
      <div className="o-dialog-head">
        <h3>{ev.text(node.title)}</h3>
        {!rt.editor && <button className="o-button o-button-link" aria-label={rt.t("Close")} onClick={() => page.closeDialog()}>×</button>}
      </div>
      <Children nodes={node.children} path={`${path}/children`} />
    </>
  );
  // In the builder, dialogs are shown in place so they can be edited.
  if (rt.editor) return <div className="o-dialog o-ed-dialog">{body}</div>;
  if (page.dialog !== name) return null;
  return (
    <div className="o-dialog-backdrop" onClick={(e) => e.target === e.currentTarget && page.closeDialog()}>
      <div role="dialog" aria-modal="true" className={withClass("o-dialog", node)}>{body}</div>
    </div>
  );
});

register("divider", ({ node }: ComponentProps) => <hr className={withClass("o-divider", node)} />);

register("heading", ({ node }: ComponentProps) => {
  const ev = useEval();
  const level = Math.min(4, Math.max(1, Number(node.level) || 2));
  const H = `h${level}` as "h1" | "h2" | "h3" | "h4";
  return <H className={withClass("o-heading", node)}>{ev.text(node.text)}</H>;
});

register("text", ({ node }: ComponentProps) => {
  const ev = useEval();
  return <p className={withClass(cls("o-text", !!node.tone && `o-tone-${node.tone}`), node)}>{ev.text(node.text)}</p>;
});

register("badge", ({ node }: ComponentProps) => {
  const ev = useEval();
  const t = ev.text(node.text);
  if (!t) return null;
  return <span className={withClass(cls("o-badge", `o-tone-${node.tone ?? "default"}`), node)}>{t}</span>;
});

/** safeURL allows same-origin paths and http(s)/mailto links only. */
export function safeURL(u: string): string | undefined {
  if (u.startsWith("assets/")) return `/assets/app/${u.slice("assets/".length)}`;
  if (u.startsWith("/") && !u.startsWith("//")) return u;
  try {
    const p = new URL(u);
    return p.protocol === "https:" || p.protocol === "http:" || p.protocol === "mailto:" ? p.href : undefined;
  } catch {
    return undefined;
  }
}

register("image", ({ node }: ComponentProps) => {
  const ev = useEval();
  const src = safeURL(ev.plain(node.src));
  if (!src) return null;
  return <img className={withClass("o-image", node)} src={src} alt={ev.text(node.alt)} width={Number(node.width) || undefined} />;
});

register("link", ({ node }: ComponentProps) => {
  const ev = useEval();
  const rt = useRuntime();
  const text = ev.text(node.text);
  const className = withClass("o-link", node);
  if (typeof node.page === "string") {
    const params: Record<string, unknown> = {};
    for (const [k, v] of Object.entries((node.params as Record<string, unknown>) || {})) params[k] = ev.ex(v);
    return (
      <a className={className} href={rt.href(node.page, params)}
        onClick={(e) => {
          if (e.metaKey || e.ctrlKey || e.shiftKey) return;
          e.preventDefault();
          rt.navigate(node.page as string, params);
        }}>
        {text}
      </a>
    );
  }
  const url = safeURL(ev.plain(node.url));
  if (!url) return <span className={className}>{text}</span>;
  // download saves instead of opening; browsers honour it for same-origin
  // URLs such as signed blob links (/v1/blobs/...).
  if (node.download !== undefined) return <a className={className} href={url} download={ev.plain(node.download)}>{text}</a>;
  return <a className={className} href={url} target="_blank" rel="noopener noreferrer">{text}</a>;
});

// Markdown: paragraphs, headings, lists, code, emphasis, and links. Raw
// HTML is shown as text; output is React nodes, never innerHTML.
function inline(s: string, key = 0): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*]+\*|_[^_]+_)|(\[[^\]]+\]\([^)\s]+\))/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) {
    if (m.index > last) out.push(s.slice(last, m.index));
    const t = m[0];
    const k = `${key}-${m.index}`;
    if (m[1]) out.push(<code key={k}>{t.slice(1, -1)}</code>);
    else if (m[2]) out.push(<strong key={k}>{inline(t.slice(2, -2), key + 1)}</strong>);
    else if (m[3]) out.push(<em key={k}>{inline(t.slice(1, -1), key + 1)}</em>);
    else {
      const [, label, href] = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(t)!;
      const url = safeURL(href);
      out.push(url ? <a key={k} href={url} target="_blank" rel="noopener noreferrer">{label}</a> : label);
    }
    last = m.index + t.length;
  }
  if (last < s.length) out.push(s.slice(last));
  return out;
}

export function Markdown({ text }: { text: string }) {
  const blocks: ReactNode[] = [];
  const lines = text.replace(/\r/g, "").split("\n");
  for (let i = 0; i < lines.length; ) {
    const l = lines[i];
    if (!l.trim()) { i++; continue; }
    if (l.startsWith("```")) {
      const body: string[] = [];
      for (i++; i < lines.length && !lines[i].startsWith("```"); i++) body.push(lines[i]);
      i++;
      blocks.push(<pre key={i}><code>{body.join("\n")}</code></pre>);
      continue;
    }
    const h = /^(#{1,4})\s+(.*)$/.exec(l);
    if (h) {
      const H = `h${h[1].length + 2 > 6 ? 6 : h[1].length + 2}` as "h3";
      blocks.push(<H key={i}>{inline(h[2])}</H>);
      i++;
      continue;
    }
    if (/^\s*([-*]|\d+\.)\s+/.test(l)) {
      const ordered = /^\s*\d+\./.test(l);
      const items: ReactNode[] = [];
      for (; i < lines.length && /^\s*([-*]|\d+\.)\s+/.test(lines[i]); i++) {
        items.push(<li key={i}>{inline(lines[i].replace(/^\s*([-*]|\d+\.)\s+/, ""))}</li>);
      }
      blocks.push(ordered ? <ol key={i}>{items}</ol> : <ul key={i}>{items}</ul>);
      continue;
    }
    const para: string[] = [];
    for (; i < lines.length && lines[i].trim() && !/^(#{1,4}\s|```|\s*([-*]|\d+\.)\s)/.test(lines[i]); i++) para.push(lines[i]);
    blocks.push(<p key={i}>{inline(para.join(" "))}</p>);
  }
  return <>{blocks}</>;
}

register("markdown", ({ node }: ComponentProps) => {
  const ev = useEval();
  return (
    <div className={withClass("o-markdown", node)}>
      <Markdown text={ev.text(node.text)} />
    </div>
  );
});
