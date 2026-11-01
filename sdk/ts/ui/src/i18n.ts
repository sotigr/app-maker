// Translations (ADR 0018). Texts are their own keys: the bundle carries,
// for the active language, the translations of the texts it contains;
// "@key" texts are explicit keys, and "@@" escapes a literal "@".
import type { I18nDoc } from "./types";

/** Translate returns a text's translation (before {{ }} interpolation). */
export type Translate = (text: string, page?: string) => string;

export function translator(i18n: I18nDoc | undefined): Translate {
  return (text, page) => {
    if (!text) return text;
    const t = (page !== undefined ? i18n?.pages?.[page]?.[text] : undefined) ?? i18n?.messages[text];
    if (t) return t;
    return text.startsWith("@@") ? text.slice(1) : text;
  };
}

let locale: string | undefined;

/** setLocale sets the language of dates and numbers (undefined: the browser's). */
export function setLocale(l: string | undefined): void {
  locale = l;
}

export function currentLocale(): string | undefined {
  return locale;
}

const RTL = new Set(["ar", "ckb", "dv", "fa", "he", "ps", "sd", "ug", "ur", "yi"]);

export function direction(lang: string): "rtl" | "ltr" {
  return RTL.has(lang.split("-")[0].toLowerCase()) ? "rtl" : "ltr";
}

/** languageName is a language's name in that language ("el" → "Ελληνικά"). */
export function languageName(lang: string): string {
  try {
    const n = new Intl.DisplayNames([lang], { type: "language" }).of(lang);
    return n ? n.charAt(0).toLocaleUpperCase(lang) + n.slice(1) : lang;
  } catch {
    return lang;
  }
}

const stored = "organon.lang";

/** savedLanguage is the language chosen in this browser, if any. */
export function savedLanguage(): string | undefined {
  try {
    return localStorage.getItem(stored) || undefined;
  } catch {
    return undefined;
  }
}

export function saveLanguage(lang: string): void {
  try {
    localStorage.setItem(stored, lang);
  } catch {
    // Private windows may refuse storage; the choice lasts for this page.
  }
}
