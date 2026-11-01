// The UI expression interpreter (ADR 0016). It evaluates the subset of
// expr (https://expr-lang.org) that `organon validate` accepts in ui/
// files, with expr's operator precedences. Shared vectors in
// testdata/uiexpr/cases.json keep it in agreement with the server.
//
// One deliberate difference: member access on nil gives nil instead of an
// error, because page data arrives asynchronously.

export type Value = unknown;
export type Env = Record<string, Value>;
export type Funcs = Record<string, (...args: Value[]) => Value>;

export class ExprError extends Error {}

type Node =
  | { k: "lit"; v: Value }
  | { k: "id"; name: string }
  | { k: "member"; obj: Node; prop: Node; optional: boolean }
  | { k: "call"; name: string; args: Node[] }
  | { k: "unary"; op: string; x: Node }
  | { k: "binary"; op: string; l: Node; r: Node }
  | { k: "cond"; c: Node; a: Node; b: Node }
  | { k: "array"; items: Node[] }
  | { k: "map"; pairs: [Node, Node][] };

type Tok = { t: "num" | "str" | "id" | "op" | "eof"; v: string; pos: number };

const BINARY: Record<string, number> = {
  or: 10, "||": 10, and: 15, "&&": 15,
  "==": 20, "!=": 20, "<": 20, ">": 20, ">=": 20, "<=": 20,
  in: 20, "not in": 20, contains: 20, startsWith: 20, endsWith: 20,
  "+": 30, "-": 30, "*": 60, "/": 60, "%": 60, "??": 500,
};
const UNARY: Record<string, number> = { not: 50, "!": 50, "-": 90, "+": 90 };
const WORD_OPS = new Set(["or", "and", "in", "not", "contains", "startsWith", "endsWith"]);
const OPS = ["?.", "??", "==", "!=", "<=", ">=", "&&", "||", "<", ">", "+", "-", "*", "/", "%", "!", "?", ":", ".", ",", "(", ")", "[", "]", "{", "}"];

function lex(src: string): Tok[] {
  const out: Tok[] = [];
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    if (/\s/.test(c)) { i++; continue; }
    if (/[0-9]/.test(c) || (c === "." && /[0-9]/.test(src[i + 1] || ""))) {
      const m = /^(?:[0-9][0-9_]*)?(?:\.[0-9][0-9_]*)?(?:[eE][+-]?[0-9]+)?/.exec(src.slice(i))!;
      out.push({ t: "num", v: m[0].replace(/_/g, ""), pos: i });
      i += m[0].length;
      continue;
    }
    if (c === "'" || c === '"') {
      let j = i + 1, s = "";
      while (j < src.length && src[j] !== c) {
        if (src[j] === "\\") {
          const e = src[++j];
          if (e === "u") { s += String.fromCharCode(parseInt(src.slice(j + 1, j + 5), 16)); j += 5; continue; }
          s += ({ n: "\n", t: "\t", r: "\r", "\\": "\\", "'": "'", '"': '"' } as Record<string, string>)[e] ?? e;
          j++;
          continue;
        }
        s += src[j++];
      }
      if (j >= src.length) throw new ExprError("unterminated string");
      out.push({ t: "str", v: s, pos: i });
      i = j + 1;
      continue;
    }
    if (/[A-Za-z_$]/.test(c)) {
      const m = /^[A-Za-z_$][A-Za-z0-9_$]*/.exec(src.slice(i))!;
      out.push({ t: WORD_OPS.has(m[0]) ? "op" : "id", v: m[0], pos: i });
      i += m[0].length;
      continue;
    }
    const op = OPS.find((o) => src.startsWith(o, i));
    if (!op) throw new ExprError(`unexpected ${c}`);
    out.push({ t: "op", v: op, pos: i });
    i += op.length;
  }
  out.push({ t: "eof", v: "", pos: src.length });
  return out;
}

class Parser {
  private i = 0;
  private toks: Tok[];
  constructor(toks: Tok[]) {
    this.toks = toks;
  }
  private get cur() { return this.toks[this.i]; }
  private is(v: string) { return this.cur.t === "op" && this.cur.v === v; }
  private expect(v: string) {
    if (!this.is(v)) throw new ExprError(`expected ${v}`);
    this.i++;
  }
  parse(): Node {
    const n = this.expr(0);
    if (this.cur.t !== "eof") throw new ExprError(`unexpected ${this.cur.v}`);
    return n;
  }
  private expr(prec: number): Node {
    let left: Node;
    const u = this.cur.t === "op" ? UNARY[this.cur.v] : undefined;
    if (u !== undefined) {
      const op = this.cur.v;
      this.i++;
      left = { k: "unary", op, x: this.expr(u) };
    } else {
      left = this.postfix(this.primary());
    }
    for (;;) {
      let op = this.cur.t === "op" ? this.cur.v : "";
      let width = 1;
      if (op === "not" && this.toks[this.i + 1].v === "in") { op = "not in"; width = 2; }
      const p = BINARY[op];
      if (p === undefined || p <= prec) break;
      this.i += width;
      left = { k: "binary", op, l: left, r: this.expr(p) };
    }
    if (prec === 0 && this.is("?")) {
      this.i++;
      const a = this.expr(0);
      this.expect(":");
      const b = this.expr(0);
      left = { k: "cond", c: left, a, b };
    }
    return left;
  }
  private primary(): Node {
    const t = this.cur;
    this.i++;
    switch (t.t) {
      case "num": return { k: "lit", v: Number(t.v) };
      case "str": return { k: "lit", v: t.v };
      case "id":
        if (t.v === "nil") return { k: "lit", v: null };
        if (t.v === "true" || t.v === "false") return { k: "lit", v: t.v === "true" };
        if (this.is("(")) {
          this.i++;
          const args: Node[] = [];
          while (!this.is(")")) {
            args.push(this.expr(0));
            if (!this.is(")")) this.expect(",");
          }
          this.i++;
          return { k: "call", name: t.v, args };
        }
        return { k: "id", name: t.v };
      case "op":
        if (t.v === "(") {
          const n = this.expr(0);
          this.expect(")");
          return n;
        }
        if (t.v === "[") {
          const items: Node[] = [];
          while (!this.is("]")) {
            items.push(this.expr(0));
            if (!this.is("]")) this.expect(",");
          }
          this.i++;
          return { k: "array", items };
        }
        if (t.v === "{") {
          const pairs: [Node, Node][] = [];
          while (!this.is("}")) {
            const kt = this.cur;
            this.i++;
            const key: Node = kt.t === "id" || kt.t === "str" ? { k: "lit", v: kt.v } : kt.t === "num" ? { k: "lit", v: Number(kt.v) } : (() => { throw new ExprError("bad map key"); })();
            this.expect(":");
            pairs.push([key, this.expr(0)]);
            if (!this.is("}")) this.expect(",");
          }
          this.i++;
          return { k: "map", pairs };
        }
    }
    throw new ExprError(`unexpected ${t.v || "end"}`);
  }
  private postfix(n: Node): Node {
    for (;;) {
      if (this.is(".") || this.is("?.")) {
        const optional = this.cur.v === "?.";
        this.i++;
        if (optional && this.is("[")) continue;
        const name = this.cur;
        if (name.t !== "id" && !(name.t === "op" && WORD_OPS.has(name.v))) throw new ExprError("expected a name after .");
        this.i++;
        n = { k: "member", obj: n, prop: { k: "lit", v: name.v }, optional };
      } else if (this.is("[")) {
        this.i++;
        const prop = this.expr(0);
        this.expect("]");
        n = { k: "member", obj: n, prop, optional: false };
      } else return n;
    }
  }
}

const cache = new Map<string, Node>();

/** parse compiles src once; repeated calls are cached. */
export function parse(src: string): Node {
  let n = cache.get(src);
  if (!n) {
    n = new Parser(lex(src)).parse();
    if (cache.size > 5000) cache.clear();
    cache.set(src, n);
  }
  return n;
}

const isObj = (v: Value): v is Record<string, Value> => typeof v === "object" && v !== null && !Array.isArray(v) && !(v instanceof Date);

function toTime(v: Value): number | undefined {
  if (v instanceof Date) return v.getTime();
  if (typeof v === "string" && /^\d{4}-\d{2}-\d{2}T/.test(v)) {
    const t = Date.parse(v);
    return isNaN(t) ? undefined : t;
  }
  return undefined;
}

/** equal follows expr: numbers by value, lists and maps deeply. */
export function equal(a: Value, b: Value): boolean {
  if (a === b) return true;
  if (a == null || b == null) return a == b;
  if (a instanceof Date || b instanceof Date) return toTime(a) === toTime(b);
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => equal(x, b[i]));
  if (isObj(a) && isObj(b)) {
    const ka = Object.keys(a), kb = Object.keys(b);
    return ka.length === kb.length && ka.every((k) => equal(a[k], b[k]));
  }
  return false;
}

function compare(op: string, a: Value, b: Value): boolean {
  let x: number | string, y: number | string;
  if (typeof a === "number" && typeof b === "number") [x, y] = [a, b];
  else if (a instanceof Date || b instanceof Date) {
    const ta = toTime(a), tb = toTime(b);
    if (ta === undefined || tb === undefined) throw new ExprError(`cannot compare ${typeof a} and ${typeof b}`);
    [x, y] = [ta, tb];
  } else if (typeof a === "string" && typeof b === "string") [x, y] = [a, b];
  else if (a == null || b == null) return false;
  else throw new ExprError(`cannot compare ${typeof a} and ${typeof b}`);
  switch (op) {
    case "<": return x < y;
    case "<=": return x <= y;
    case ">": return x > y;
    default: return x >= y;
  }
}

function member(obj: Value, key: Value): Value {
  if (obj == null) return null;
  if (Array.isArray(obj)) {
    if (typeof key !== "number") throw new ExprError("list index must be a number");
    const i = key < 0 ? obj.length + key : key;
    if (i < 0 || i >= obj.length) throw new ExprError(`index ${key} out of range`);
    return obj[i];
  }
  if (isObj(obj)) return Object.prototype.hasOwnProperty.call(obj, String(key)) ? obj[String(key)] : null;
  return null;
}

function goString(v: Value): string {
  if (v == null) return "<nil>";
  if (v instanceof Date) return v.toISOString();
  if (Array.isArray(v)) return "[" + v.map(goString).join(" ") + "]";
  if (isObj(v)) return "map[" + Object.keys(v).sort().map((k) => `${k}:${goString(v[k])}`).join(" ") + "]";
  return String(v);
}

function num(v: Value, fn: string): number {
  if (typeof v !== "number") throw new ExprError(`${fn}: want a number`);
  return v;
}

const BUILTINS: Funcs = {
  len: (v) => (typeof v === "string" ? [...v].length : Array.isArray(v) ? v.length : isObj(v) ? Object.keys(v).length : (() => { throw new ExprError("len: want a string, list, or map"); })()),
  lower: (v) => String(v ?? "").toLowerCase(),
  upper: (v) => String(v ?? "").toUpperCase(),
  trim: (v) => String(v ?? "").trim(),
  string: (v) => goString(v),
  int: (v) => {
    if (typeof v === "number") return Math.trunc(v);
    if (typeof v === "string" && /^[+-]?\d+$/.test(v.trim())) return parseInt(v, 10);
    throw new ExprError(`int: cannot convert ${goString(v)}`);
  },
  float: (v) => {
    const f = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
    if (isNaN(f)) throw new ExprError(`float: cannot convert ${goString(v)}`);
    return f;
  },
  join: (l, sep) => (Array.isArray(l) ? l.map((x) => (typeof x === "string" ? x : goString(x))).join(String(sep ?? "")) : ""),
  split: (s, sep) => String(s ?? "").split(String(sep)),
  abs: (v) => Math.abs(num(v, "abs")),
  round: (v) => { const n = num(v, "round"); return Math.sign(n) * Math.round(Math.abs(n)); },
  floor: (v) => Math.floor(num(v, "floor")),
  ceil: (v) => Math.ceil(num(v, "ceil")),
  min: (...a) => Math.min(...(a.length === 1 && Array.isArray(a[0]) ? a[0] : a).map((x) => num(x, "min"))),
  max: (...a) => Math.max(...(a.length === 1 && Array.isArray(a[0]) ? a[0] : a).map((x) => num(x, "max"))),
  now: () => new Date(),
};

function evalNode(n: Node, env: Env, fns: Funcs): Value {
  switch (n.k) {
    case "lit": return n.v;
    case "id":
      if (!(n.name in env)) throw new ExprError(`unknown name ${n.name}`);
      return env[n.name];
    case "member": return member(evalNode(n.obj, env, fns), evalNode(n.prop, env, fns));
    case "array": return n.items.map((x) => evalNode(x, env, fns));
    case "map": {
      const o: Record<string, Value> = {};
      for (const [k, v] of n.pairs) o[String(evalNode(k, env, fns))] = evalNode(v, env, fns);
      return o;
    }
    case "cond": return evalNode(n.c, env, fns) === true ? evalNode(n.a, env, fns) : evalNode(n.b, env, fns);
    case "call": {
      const f = fns[n.name] || BUILTINS[n.name];
      if (!f) throw new ExprError(`unknown function ${n.name}`);
      return f(...n.args.map((a) => evalNode(a, env, fns)));
    }
    case "unary": {
      const x = evalNode(n.x, env, fns);
      if (n.op === "-") return -num(x, "-");
      if (n.op === "+") return num(x, "+");
      return !(x === true);
    }
    case "binary": {
      const { op } = n;
      if (op === "&&" || op === "and") return evalNode(n.l, env, fns) === true && evalNode(n.r, env, fns) === true;
      if (op === "||" || op === "or") return evalNode(n.l, env, fns) === true || evalNode(n.r, env, fns) === true;
      if (op === "??") {
        const l = evalNode(n.l, env, fns);
        return l ?? evalNode(n.r, env, fns);
      }
      const l = evalNode(n.l, env, fns), r = evalNode(n.r, env, fns);
      switch (op) {
        case "==": return equal(l, r);
        case "!=": return !equal(l, r);
        case "<": case "<=": case ">": case ">=": return compare(op, l, r);
        case "+":
          if (typeof l === "string" && typeof r === "string") return l + r;
          if (Array.isArray(l) && Array.isArray(r)) return [...l, ...r];
          return num(l, "+") + num(r, "+");
        case "-": return num(l, "-") - num(r, "-");
        case "*": return num(l, "*") * num(r, "*");
        case "/": return num(l, "/") / num(r, "/");
        case "%": return num(l, "%") % num(r, "%");
        case "in": case "not in": {
          const has = Array.isArray(r) ? r.some((x) => equal(x, l)) : isObj(r) ? Object.prototype.hasOwnProperty.call(r, String(l)) : false;
          return op === "in" ? has : !has;
        }
        case "contains": return typeof l === "string" && typeof r === "string" && l.includes(r);
        case "startsWith": return typeof l === "string" && typeof r === "string" && l.startsWith(r);
        case "endsWith": return typeof l === "string" && typeof r === "string" && l.endsWith(r);
      }
    }
  }
  throw new ExprError("unsupported expression");
}

/** evaluate runs src in env; fns adds or overrides functions (can, formatDate, ...). */
export function evaluate(src: string, env: Env, fns: Funcs = {}): Value {
  return evalNode(parse(src), env, fns);
}

/** template renders text with {{ expr }} interpolations; nil renders empty. */
export function template(text: string, env: Env, fns: Funcs = {}, onError?: (e: unknown, src: string) => void): string {
  if (!text.includes("{{")) return text;
  return text.replace(/\{\{([\s\S]*?)\}\}/g, (_, src: string) => {
    try {
      const v = evaluate(src.trim(), env, fns);
      if (v == null) return "";
      if (v instanceof Date) return v.toLocaleString();
      return typeof v === "string" ? v : goString(v);
    } catch (e) {
      onError?.(e, src);
      return "";
    }
  });
}
