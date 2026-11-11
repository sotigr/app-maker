// Pure helpers of dictation (ADR 0028): the recognition language of the
// user's UI language, and inserting recognized text into a field's value.
// No DOM or React here, so node tests run them directly.

/** Default regions for languages whose browser tag has none ("el" → "el-GR"). */
const REGION: Record<string, string> = {
  ar: "ar-SA", bg: "bg-BG", cs: "cs-CZ", da: "da-DK", de: "de-DE", el: "el-GR", en: "en-US", es: "es-ES",
  fi: "fi-FI", fr: "fr-FR", he: "he-IL", hr: "hr-HR", hu: "hu-HU", it: "it-IT", ja: "ja-JP", ko: "ko-KR",
  nb: "nb-NO", nl: "nl-NL", pl: "pl-PL", pt: "pt-PT", ro: "ro-RO", ru: "ru-RU", sk: "sk-SK", sl: "sl-SI",
  sq: "sq-AL", sr: "sr-RS", sv: "sv-SE", tr: "tr-TR", uk: "uk-UA", zh: "zh-CN",
};

/**
 * speechLang returns the BCP 47 tag browser recognition needs for the UI
 * language: the language itself when it has a region, else the matching
 * browser language ("el" + ["el-CY"] → "el-CY"), else a default region.
 * Without a UI language it uses the browser's.
 */
export function speechLang(uiLang: string | undefined, browser: readonly string[] = []): string {
  const lang = uiLang || browser[0] || "en";
  if (lang.includes("-")) return lang;
  const base = lang.toLowerCase();
  const match = browser.find((b) => b.includes("-") && b.toLowerCase().split("-")[0] === base);
  return match || REGION[base] || lang;
}

/** speechBase is the primary language subtag servers expect ("el-GR" → "el"). */
export function speechBase(tag: string): string {
  return tag.split("-")[0].toLowerCase();
}

// A sentence ends with these (the Greek question mark is ";" or U+037E).
const SENTENCE_END = /[.!?;;…:\n]\s*$/;
// Text starting with punctuation attaches to the previous word.
const PUNCT_START = /^[,.!?;:;…)\]}»”]/;

/**
 * insertAt puts recognized text into value in place of [start, end):
 * spaced from its neighbours, and capitalized at the start of the value
 * or after a sentence end. It returns the new value and the caret after
 * the inserted text.
 */
export function insertAt(value: string, start: number, end: number, text: string, lang?: string): { value: string; caret: number } {
  text = text.trim().replace(/\s+/g, " ");
  start = Math.max(0, Math.min(start, value.length));
  end = Math.max(start, Math.min(end, value.length));
  if (!text) return { value, caret: start };
  const before = value.slice(0, start);
  const after = value.slice(end);
  if (before.trim() === "" || SENTENCE_END.test(before)) {
    // Greek all-caps rules drop accents ("ό" → "Ο"); a capital initial keeps
    // them ("Όλα"). Other languages (Turkish "i" → "İ") need their locale.
    const first = text.charAt(0);
    text = (lang && !lang.toLowerCase().startsWith("el") ? first.toLocaleUpperCase(lang) : first.toUpperCase()) + text.slice(1);
  }
  const lead = before !== "" && !/\s$/.test(before) && !PUNCT_START.test(text) ? " " : "";
  const trail = after !== "" && !/^\s/.test(after) && !PUNCT_START.test(after) ? " " : "";
  const head = before + lead + text;
  return { value: head + trail + after, caret: head.length };
}
