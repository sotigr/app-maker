// Page documents as trees of component nodes addressed by JSON Pointers
// (the same pointers organon validate reports), for the UI builder.
import type { UiCatalog, UiComponent } from "./api";

export type Doc = Record<string, any>;

const unesc = (s: string) => s.replace(/~1/g, "/").replace(/~0/g, "~");

export function parts(ptr: string): string[] {
  return ptr === "" ? [] : ptr.slice(1).split("/").map(unesc);
}

export function getAt(doc: any, ptr: string): any {
  let cur = doc;
  for (const p of parts(ptr)) {
    if (cur == null) return undefined;
    cur = cur[Array.isArray(cur) ? Number(p) : p];
  }
  return cur;
}

/** setAt returns a copy of doc with value at ptr (undefined removes a member). */
export function setAt(doc: Doc, ptr: string, value: any): Doc {
  const out = structuredClone(doc);
  const ps = parts(ptr);
  let cur: any = out;
  for (const p of ps.slice(0, -1)) cur = cur[Array.isArray(cur) ? Number(p) : p];
  const last = ps[ps.length - 1];
  if (Array.isArray(cur)) cur[Number(last)] = value;
  else if (value === undefined) delete cur[last];
  else cur[last] = value;
  return out;
}

export function parentList(ptr: string): { list: string; index: number } | null {
  const i = ptr.lastIndexOf("/");
  const idx = Number(ptr.slice(i + 1));
  return Number.isInteger(idx) && ptr.slice(i + 1) !== "" ? { list: ptr.slice(0, i), index: idx } : null;
}

export function removeAt(doc: Doc, ptr: string): Doc {
  const pl = parentList(ptr);
  if (!pl) return setAt(doc, ptr, undefined);
  const list = [...(getAt(doc, pl.list) as any[])];
  list.splice(pl.index, 1);
  return setAt(doc, pl.list, list);
}

export function insertAt(doc: Doc, listPtr: string, index: number, value: any): Doc {
  const list = [...((getAt(doc, listPtr) as any[]) || [])];
  list.splice(Math.max(0, Math.min(index, list.length)), 0, value);
  return setAt(doc, listPtr, list);
}

/** Slot is a place in a node that holds child nodes. */
export interface Slot {
  ptr: string;
  label: string;
  many: boolean;
}

export function component(cat: UiCatalog | undefined, name: string): UiComponent | undefined {
  return cat?.components.find((c) => c.name === name);
}

export function slots(cat: UiCatalog | undefined, node: any, ptr: string): Slot[] {
  const c = component(cat, node?.component);
  const out: Slot[] = [];
  for (const p of c?.props || []) {
    if (p.kind === "children") out.push({ ptr: `${ptr}/${p.name}`, label: p.name, many: true });
    else if (p.kind === "node") out.push({ ptr: `${ptr}/${p.name}`, label: p.name, many: false });
    else if (p.kind === "objects" && p.item?.some((i) => i.kind === "children")) {
      (node[p.name] || []).forEach((it: any, i: number) => {
        out.push({ ptr: `${ptr}/${p.name}/${i}/children`, label: `${p.name} ${i + 1}${it?.label ? `: ${it.label}` : ""}`, many: true });
      });
    }
  }
  return out;
}

export interface Entry {
  ptr: string;
  node: any;
  depth: number;
  slot?: string; // label of the slot when not "children"
}

/** outline lists every node in document order. */
export function outline(cat: UiCatalog | undefined, doc: Doc): Entry[] {
  const out: Entry[] = [];
  const visit = (node: any, ptr: string, depth: number, slot?: string) => {
    if (!node || typeof node !== "object") return;
    out.push({ ptr, node, depth, slot });
    for (const s of slots(cat, node, ptr)) {
      const v = getAt(doc, s.ptr);
      if (s.many) (Array.isArray(v) ? v : []).forEach((n, i) => visit(n, `${s.ptr}/${i}`, depth + 1, s.label === "children" ? undefined : s.label));
      else visit(v, s.ptr, depth + 1, s.label);
    }
  };
  visit(doc.body, "/body", 0);
  return out;
}

/** nodeFor maps a problem pointer to the deepest node containing it. */
export function nodeFor(entries: Entry[], pointer: string): string | undefined {
  let best: string | undefined;
  for (const e of entries) {
    if ((pointer === e.ptr || pointer.startsWith(e.ptr + "/")) && (!best || e.ptr.length > best.length)) best = e.ptr;
  }
  return best;
}

/** move relocates the node at from to index of listPtr. */
export function move(doc: Doc, from: string, listPtr: string, index: number): Doc {
  if (listPtr === from || listPtr.startsWith(from + "/")) return doc; // into itself
  const node = getAt(doc, from);
  const src = parentList(from);
  let out = removeAt(doc, from);
  let target = listPtr;
  let at = index;
  if (src) {
    // Removing from an earlier position shifts later siblings and nested pointers.
    if (src.list === listPtr && src.index < index) at -= 1;
    const prefix = `${src.list}/`;
    if (target.startsWith(prefix)) {
      const rest = target.slice(prefix.length).split("/");
      const i = Number(rest[0]);
      if (i > src.index) target = `${prefix}${i - 1}${rest.length > 1 ? "/" + rest.slice(1).join("/") : ""}`;
    }
  }
  out = insertAt(out, target, at, node);
  return out;
}

/** summary is a one-line description of a node for the outline. */
export function summary(node: any): string {
  for (const k of ["text", "label", "title", "name", "source", "type", "kind"]) {
    if (typeof node?.[k] === "string" && node[k]) return node[k].length > 40 ? node[k].slice(0, 40) + "…" : node[k];
  }
  return "";
}

/** blank returns a new node of component c with its required props filled. */
export function blank(c: UiComponent, sources: { name: string; list: boolean }[], fields: string[]): any {
  const node: any = { component: c.name };
  for (const p of c.props || []) {
    if (!p.required && p.kind !== "children") continue;
    switch (p.kind) {
      case "text":
        node[p.name] = p.name === "text" || p.name === "label" ? c.name[0].toUpperCase() + c.name.slice(1) : "";
        break;
      case "expr":
        node[p.name] = p.want === "bool" ? "true" : "0";
        break;
      case "enum":
        node[p.name] = p.enum?.[0];
        break;
      case "children":
        node[p.name] = [];
        break;
      case "node":
        node[p.name] = { component: "text", text: "{{ item.id }}" };
        break;
      case "source": {
        const s = sources.find((x) => x.list === (c.source === "list")) || sources[0];
        if (s) node[p.name] = s.name;
        break;
      }
      case "path":
        node[p.name] = fields[0] || "/id";
        break;
      case "objects":
        node[p.name] = [Object.fromEntries((p.item || []).filter((i) => i.required || i.name === "field").map((i) => [i.name,
          i.kind === "children" ? [] : i.kind === "path" ? fields[0] || "/id" : i.kind === "steps" ? [] : i.kind === "expr" ? "0" : "Item"]))];
        break;
      case "steps":
        node[p.name] = [];
        break;
      default:
        node[p.name] = "";
    }
  }
  if (c.events?.length) node.on = { [c.events[0]]: [] };
  return node;
}
