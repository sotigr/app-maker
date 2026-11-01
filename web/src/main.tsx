import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { OrganonApp } from "@organon/ui";
import "@organon/ui/base.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <OrganonApp />
  </StrictMode>,
);
