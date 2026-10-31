import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Served by fwd at /admin (embedded); `pnpm dev` proxies the API.
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 1500 },
  server: { proxy: { "/v1": "http://localhost:8080" } },
});
