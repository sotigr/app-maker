// Shapes of the UI bundle served at GET /v1/ui and of ui/ documents
// (ADR 0016). The server validates documents; the renderer trusts them.

export interface Principal {
  id: string;
  kind: string;
  roles: string[];
  /** The user's saved UI language. */
  locale?: string;
  /** The shared feature, for share-link holders (kind "link"). */
  object?: string;
}

export interface NavItem {
  label: string;
  page: string;
  params?: Record<string, unknown>;
}

export interface AppDoc {
  title?: string;
  logo?: string;
  home: string;
  /** The page share-link holders open, with params.id = the shared feature. */
  linkHome?: string;
  nav?: NavItem[];
  requireLogin?: boolean;
  notFound?: string;
  /** Dictation into text fields (ADR 0028); absent when off. */
  speech?: import("./speech").SpeechConfig;
}

export interface ParamSpec {
  type: "string" | "integer" | "number" | "boolean";
  format?: string;
}

export type Expr = string | number | boolean | null;

export interface StepDoc {
  do: string;
  onError?: StepDoc[];
  [prop: string]: unknown;
}

export interface NodeDoc {
  component: string;
  visible?: Expr;
  class?: string;
  on?: Record<string, StepDoc[]>;
  [prop: string]: unknown;
}

export interface Predicate {
  field?: string;
  op?: string;
  value?: Expr;
  and?: Predicate[];
  or?: Predicate[];
  not?: Predicate;
}

export interface QuerySource {
  type: string;
  where?: Predicate;
  sort?: { field: string; dir?: "asc" | "desc" }[];
  scope?: { childrenOf?: Expr; descendantsOf?: Expr };
  limit?: number;
}

export interface SourceDoc {
  feature?: Expr;
  children?: Expr;
  search?: Expr;
  query?: QuerySource;
  type?: string;
  limit?: number;
}

export interface PageDoc {
  page: string;
  title?: string;
  route: string;
  params?: Record<string, ParamSpec>;
  visible?: Expr;
  state?: Record<string, { type: string; default?: unknown }>;
  data?: Record<string, SourceDoc>;
  body: NodeDoc;
}

export interface PageDTO {
  name: string;
  route: string;
  params: Record<string, ParamSpec> | null;
  doc: PageDoc;
}

export interface FieldMeta {
  kind: string;
  title?: string;
  enum?: string[];
  ref?: string[];
  nullable?: boolean;
  required?: boolean;
  readOnly?: boolean;
  format?: string;
  thumbnails?: boolean;
  /** Translated enum values. */
  labels?: Record<string, string>;
}

export interface StateMachine {
  initial: string;
  transitions: { from: string; to: string; action: string }[];
}

export interface TypeMeta {
  name: string;
  title?: string;
  fields: Record<string, FieldMeta>;
  stateMachines?: Record<string, StateMachine>;
  children?: string[];
  ownable?: boolean;
  /** Translated transition buttons. */
  actionLabels?: Record<string, string>;
  /** Action name to input JSON Schema (null for built-in actions). */
  actions: Record<string, JsonSchema | null>;
}

export interface JsonSchema {
  type?: string | string[];
  properties?: Record<string, JsonSchema>;
  required?: string[];
  enum?: unknown[];
  title?: string;
  description?: string;
  format?: string;
  maxLength?: number;
  [k: string]: unknown;
}

export interface Bundle {
  version: string;
  app: AppDoc;
  loginRequired: boolean;
  pages: PageDTO[];
  types: Record<string, TypeMeta>;
  styles?: string;
  i18n?: I18nDoc;
}

/** The bundle's language and its translations (ADR 0018). */
export interface I18nDoc {
  language: string;
  languages: string[];
  messages: Record<string, string>;
  /** Per-page overrides of messages. */
  pages?: Record<string, Record<string, string>>;
}

/** A feature as UI expressions see it: its fields plus id, type, parentId, version. */
export type Row = Record<string, unknown> & { id: string; type: string; parentId: string | null; version: number };
