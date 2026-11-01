// @organon/ui: renders the end-user UI described by a manifest's ui/ files
// (ADR 0016). The server validates the documents and serves the bundle at
// GET /v1/ui; this package interprets it on @organon/client.
import "./components/layout";
import "./components/data";
import "./components/forms";
import "./components/charts";

export { OrganonApp, PageView, RuntimeProvider, resolve, type OrganonAppProps, type RuntimeProviderProps } from "./app";
export { evaluate, template, parse, ExprError } from "./expr";
export { Node, register, type ComponentProps } from "./render";
export { buildPath, matchRoute, type EditorHooks, type Runtime } from "./runtime";
export type * from "./types";
