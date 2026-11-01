// The node renderer: looks up each node's component in the registry,
// applies `visible`, and in builder mode wraps nodes in selectable frames.
import { Component, useMemo, type ComponentType, type ReactNode } from "react";
import { ScopeContext, useEval, useRuntime, useScope } from "./runtime";
import type { Env } from "./expr";
import type { NodeDoc } from "./types";

export interface ComponentProps {
  node: NodeDoc;
  /** JSON Pointer of the node in its page document. */
  path: string;
}

const registry: Record<string, ComponentType<ComponentProps>> = {};

/** register adds a component implementation (the catalog is fixed by the server). */
export function register(name: string, c: ComponentType<ComponentProps>): void {
  registry[name] = c;
}

class Boundary extends Component<{ path: string; children: ReactNode }, { error?: Error }> {
  state: { error?: Error } = {};
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  render() {
    if (this.state.error) return <div className="o-error">This part of the page failed: {this.state.error.message}</div>;
    return this.props.children;
  }
}

export function Node({ node, path }: ComponentProps) {
  const rt = useRuntime();
  const ev = useEval();
  const C = registry[node.component];
  const hidden = node.visible !== undefined && ev.ex(node.visible) !== true;
  const ed = rt.editor;
  if (!ed) {
    if (hidden || !C) return null;
    return (
      <Boundary path={path}>
        <C node={node} path={path} />
      </Boundary>
    );
  }
  // Builder preview: hidden nodes stay selectable but faded.
  const cls = ["o-ed-frame", ed.selected === path ? "o-ed-selected" : "", hidden ? "o-ed-hidden" : "", ed.problems?.has(path) ? "o-ed-bad" : ""]
    .filter(Boolean).join(" ");
  return (
    <div className={cls} data-ui-path={path} data-ui-component={node.component}
      onClickCapture={(e) => {
        if ((e.target as HTMLElement).closest("[data-ui-path]") === e.currentTarget) {
          e.preventDefault();
          e.stopPropagation();
          ed.onSelect(path);
        }
      }}>
      <span className="o-ed-tag">{node.component}</span>
      <Boundary path={path}>{C ? <C node={node} path={path} /> : <div className="o-error">Unknown component {node.component}</div>}</Boundary>
    </div>
  );
}

export function Children({ nodes, path }: { nodes: unknown; path: string }) {
  if (!Array.isArray(nodes)) return null;
  return (
    <>
      {nodes.map((n, i) => (
        <Node key={i} node={n as NodeDoc} path={`${path}/${i}`} />
      ))}
    </>
  );
}

/** WithScope adds variables (item, index, result, ...) for its subtree. */
export function WithScope({ vars, children }: { vars: Env; children: ReactNode }) {
  const parent = useScope();
  const env = useMemo(() => ({ ...parent, ...vars }), [parent, ...Object.values(vars)]);
  return <ScopeContext.Provider value={env}>{children}</ScopeContext.Provider>;
}
