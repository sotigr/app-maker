// Checks that the renderer's own texts, the literals passed to t(...),
// are exactly internal/ui/domain/texts.json, which validate and
// `organon ui i18n` use (ADR 0018). Usage: node sdk/ts/ui/test/texts.test.ts
import { readdirSync, readFileSync } from "node:fs";

const listed: string[] = JSON.parse(readFileSync(new URL("../../../../internal/ui/domain/texts.json", import.meta.url), "utf8"));
const used = new Set<string>();
const dirs = ["../src/", "../src/components/"];
for (const d of dirs) {
  const dir = new URL(d, import.meta.url);
  for (const f of readdirSync(dir).filter((n) => /\.tsx?$/.test(n))) {
    const src = readFileSync(new URL(f, dir), "utf8");
    for (const call of src.matchAll(/\bt\(([^()]*)\)/g)) {
      for (const lit of call[1].matchAll(/"((?:[^"\\]|\\.)*)"/g)) used.add(JSON.parse(`"${lit[1]}"`));
    }
  }
}
let failed = 0;
for (const t of used) {
  if (!listed.includes(t)) {
    failed++;
    console.log(`FAIL: ${JSON.stringify(t)} is used but not in texts.json`);
  }
}
for (const t of listed) {
  if (!used.has(t)) {
    failed++;
    console.log(`FAIL: ${JSON.stringify(t)} is in texts.json but not used`);
  }
}
console.log(`${used.size} texts, ${failed} failed`);
if (failed) process.exit(1);
