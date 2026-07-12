import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The tracker Go binary embeds dist/ and serves it at "/" (see web/web.go). Same-origin BFF under /api.
export default defineConfig({
  base: "/",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    // Local dev: proxy the BFF so `npm run dev` can hit a locally-running tracker binary.
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
