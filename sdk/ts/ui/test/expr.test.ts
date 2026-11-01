// Runs the shared expression vectors (testdata/uiexpr/cases.json) against
// the browser interpreter; the Go test TestExpressionVectors runs the same
// file against the server. Usage: node sdk/ts/ui/test/expr.test.ts
import { readFileSync } from "node:fs";
import { evaluate, equal } from "../src/expr.ts";

type Case = { expr: string; env?: Record<string, unknown>; want?: unknown; reject?: boolean };
const url = new URL("../../../../testdata/uiexpr/cases.json", import.meta.url);
const cases: Case[] = JSON.parse(readFileSync(url, "utf8"));
let failed = 0;
for (const c of cases) {
  let got: unknown, err: unknown;
  try {
    got = evaluate(c.expr, c.env || {});
  } catch (e) {
    err = e;
  }
  if (c.reject) {
    if (!err) {
      failed++;
      console.error(`FAIL ${c.expr}: want rejected, got ${JSON.stringify(got)}`);
    }
  } else if (err || !equal(got, c.want ?? null)) {
    failed++;
    console.error(`FAIL ${c.expr}: got ${err ? err : JSON.stringify(got)}, want ${JSON.stringify(c.want)}`);
  }
}
console.log(`${cases.length - failed} passed, ${failed} failed`);
if (failed) process.exit(1);
