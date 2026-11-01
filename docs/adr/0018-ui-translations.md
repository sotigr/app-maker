# 0018: Translations of the end-user UI

Status: accepted (2026-11-02)

## Context
The end-user UI (ADR 0016) is written in one language. Page texts are
literal text props, the renderer has its own English texts (sign-in, Save,
empty states), field labels come from schema titles or field names, enum
values are shown as they are stored, and dates and numbers follow the
browser. The spec does not cover translation.

## Decision
Decided with the user (2026-11-02):

| Topic | Decision |
| --- | --- |
| Keys | Hybrid. A text is its own key (gettext style), so pages stay as written. A text that is exactly `@key` is an explicit key, for long or unstable texts; `@@` escapes a literal `@`. Per-page sections override a text on one page, for words with several meanings. |
| User preference | Saved in the user record: a new owner-visible, owner-writable `locale` field on the system `user` type (BCP 47 pattern). |
| Scope | The UI only. Server messages (validation errors, `reject` messages, emails) stay untranslated. |
| Example | The tracker gets Greek (`el`). |

**Files.** `ui/app.json` lists `"languages": ["en", "el"]`; the first is the
language the ui/ files are written in. Each language may have
`ui/i18n/<lang>.json`:

```json
{
  "messages": { "Projects": "Έργα", "Delete {{ data.task.title }}?": "Διαγραφή «{{ data.task.title }}»;", "@projects.intro": "…" },
  "pages":    { "projects": { "Create": "Δημιουργία έργου" } },
  "types":    { "task": { "fields": { "/status": "Κατάσταση" },
                          "values": { "/status": { "in_progress": "Σε εξέλιξη" } },
                          "actions": { "start": "Έναρξη" } } }
}
```

- The source language's file holds the text of every `@key`. It may also
  reword texts and give nicer labels, such as "In progress" for
  `in_progress`.
- Lookup order: the active language, then the source language's file,
  then the text itself.
- Field labels, enum values, and transition buttons come from the types,
  so they are translated per type with explicit keys. The schema stays
  language-neutral.
- The renderer's own texts (`internal/ui/domain/texts.json`) are
  translated in `messages` like page texts. Nothing is built in per
  language. `sdk/ts/ui/test/texts.test.ts` keeps the list and the
  renderer in sync.

**Validation (`organon validate`).**

Errors:
- language tags in `languages` and in file names;
- a file whose language is not listed;
- unknown members;
- an `@key` without its source text;
- a translation whose `{{ }}` expressions differ from its original's, as a
  multiset. The originals are type-checked where they are used, and an
  `@key`'s text is checked in each place that uses it.

Warnings:
- entries nothing uses (stale);
- unknown types, fields, enum values, or actions;
- per language, a count of missing translations.

Texts with no letters outside `{{ }}` (data only) and the URL props
`image.src`, `link.url`, and `link.download` are not translated.

**Tooling.**
- `organon ui i18n [dir] --lang <l> [--prune]` writes the language's file
  with an empty entry for everything untranslated.
- `organon ui schema` also writes `ui-i18n.json`.

**Language choice (server, `GET /v1/ui`).** The order is:
1. the user's saved `locale`;
2. the `lang` query parameter, which is the choice remembered in this
   browser;
3. `Accept-Language`, by q;
4. the first language.

Tags match exactly, or else by base language (`el-GR` → `el`).

**Bundle contents.** The bundle carries `i18n {language, languages,
messages, pages}`. It holds only the translations of the app shell, the
renderer, and the pages in the bundle, so it reveals no text of hidden
pages. Type labels arrive translated in the field metadata: `title`,
`labels` (enum values), and `actionLabels`.

**Renderer.**
- Text props are translated before `{{ }}` interpolation.
- Dates and numbers use the active language.
- `<html lang dir>` is set; right-to-left is chosen by language.
- The base CSS uses logical properties.
- A language switch appears in the header and on the sign-in page when
  there are several languages. Signed-in users save their choice to their
  user record, and the browser also keeps it in `localStorage` for the
  sign-in page.

## Consequences
- Changing a source text orphans its translations. `validate` reports the
  stale entry and the new missing one, and `organon ui i18n` adds the new
  one.
- If an app's policies forbid users updating their own user record, saving
  the choice fails and the switch shows the error.
- Plural forms, translated server messages, and editing translations in
  the builder are not done.
