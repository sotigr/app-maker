import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

const sdk = (p) => fileURLToPath(new URL(`../sdk/ts/${p}`, import.meta.url));

// The end-user UI renderer (ADR 0016), embedded in organond: index.html is
// served for every app route, build assets under /assets/organon/.
export default defineConfig({
  base: "/assets/organon/",
  plugins: [react()],
  resolve: {
    alias: [
      { find: /^@organon\/client$/, replacement: sdk("client/src/index.ts") },
      { find: /^@organon\/react$/, replacement: sdk("react/src/index.ts") },
      { find: /^@organon\/ui\/base\.css/, replacement: sdk("ui/src/base.css") },
      { find: /^@organon\/ui$/, replacement: sdk("ui/src/index.ts") },
    ],
    dedupe: ["react", "react-dom"],
  },
  build: { outDir: "dist", emptyOutDir: true, assetsDir: "." },
  server: { proxy: { "/v1": "http://localhost:8080", "/assets/app": "http://localhost:8080", "/assets/styles.css": "http://localhost:8080" } },
});
