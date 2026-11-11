// Dictation helpers (ADR 0028): the recognition language of the UI
// language, and inserting recognized text. Usage: node sdk/ts/ui/test/speech.test.ts
import { insertAt, speechBase, speechLang } from "../src/speechtext.ts";

let failed = 0;
const eq = (name: string, got: unknown, want: unknown) => {
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};

eq("region kept", speechLang("el-CY", ["en-US"]), "el-CY");
eq("browser region wins", speechLang("el", ["en-GB", "el-CY"]), "el-CY");
eq("default region", speechLang("el", ["en-US"]), "el-GR");
eq("unknown language bare", speechLang("xx", []), "xx");
eq("no UI language: browser's", speechLang(undefined, ["de-AT"]), "de-AT");
eq("base", speechBase("el-GR"), "el");

eq("empty field capitalizes", insertAt("", 0, 0, "αλλαγή λαδιών", "el"), { value: "Αλλαγή λαδιών", caret: 13 });
eq("appends with a space", insertAt("Oil change", 10, 10, "and filters"), { value: "Oil change and filters", caret: 22 });
eq("after a sentence", insertAt("Done.", 5, 5, "next step"), { value: "Done. Next step", caret: 15 });
eq("after a Greek question mark", insertAt("Τι έγινε;", 9, 9, "όλα καλά", "el"), { value: "Τι έγινε; Όλα καλά", caret: 18 });
eq("inserts in the middle", insertAt("front brakes", 5, 5, "and rear"), { value: "front and rear brakes", caret: 14 });
eq("replaces the selection", insertAt("old text here", 0, 8, "new"), { value: "New here", caret: 3 });
eq("Turkish capital i", insertAt("", 0, 0, "istanbul", "tr").value, "İstanbul");
eq("punctuation attaches", insertAt("Hello", 5, 5, ", world"), { value: "Hello, world", caret: 12 });
eq("blank speech changes nothing", insertAt("abc", 1, 1, "  "), { value: "abc", caret: 1 });
eq("clamps the range", insertAt("abc", 9, 2, "d"), { value: "abc d", caret: 5 });

console.log(`${failed ? "" : "all "}speech helper checks ${failed ? `failed: ${failed}` : "passed"}`);
if (failed) process.exit(1);
