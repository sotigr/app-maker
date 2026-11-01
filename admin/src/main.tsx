import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "@xyflow/react/dist/style.css";
import "./styles.css";
import { Api } from "./api";
import { Login } from "./pages/Login";
import { Types } from "./pages/Types";
import { FeatureList } from "./pages/FeatureList";
import { FeaturePage } from "./pages/FeaturePage";
import { Flows } from "./pages/Flows";
import { FlowEditor } from "./pages/FlowEditor";
import { Runs } from "./pages/Runs";
import { Deploys } from "./pages/Deploys";
import { Audit } from "./pages/Audit";

export type Principal = { id: string; kind: string; roles: string[] };

function useHash(): string {
  const [hash, setHash] = useState(location.hash || "#/types");
  useEffect(() => {
    const on = () => setHash(location.hash || "#/types");
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash;
}

function App() {
  const [me, setMe] = useState<Principal | null | undefined>(undefined);
  const hash = useHash();
  useEffect(() => {
    Api.session().then((s) => setMe(s.principal), () => setMe(null));
  }, []);
  if (me === undefined) return <div className="center">Loading…</div>;
  if (me === null) return <Login onLogin={() => Api.session().then((s) => setMe(s.principal))} />;
  const parts = hash.replace(/^#\//, "").split("/");
  let page;
  switch (parts[0]) {
    case "types":
      page = parts[1] ? <FeatureList type={parts[1]} /> : <Types />;
      break;
    case "features":
      page = <FeaturePage type={parts[1]} id={parts[2]} />;
      break;
    case "flows":
      page = parts[1] !== undefined ? <FlowEditor name={decodeURIComponent(parts[1])} /> : <Flows />;
      break;
    case "runs":
      page = <Runs />;
      break;
    case "deploys":
      page = <Deploys />;
      break;
    case "audit":
      page = <Audit />;
      break;
    default:
      page = <Types />;
  }
  const admin = me.roles.includes("admin") || me.kind === "system";
  const nav = (href: string, label: string) => (
    <a className={hash.startsWith(href) ? "active" : ""} href={href}>
      {label}
    </a>
  );
  return (
    <div className="shell">
      <header>
        <strong>fw admin</strong>
        <nav>
          {nav("#/types", "Data")}
          {admin && nav("#/flows", "Flows")}
          {admin && nav("#/runs", "Runs")}
          {admin && nav("#/deploys", "Deploys")}
          {admin && nav("#/audit", "Audit")}
        </nav>
        <span className="who">
          {me.kind} {me.id.slice(0, 8)} {me.roles.length ? `(${me.roles.join(", ")})` : ""}
          <button className="link" onClick={() => Api.logout().finally(() => setMe(null))}>
            Log out
          </button>
        </span>
      </header>
      <main>{page}</main>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
