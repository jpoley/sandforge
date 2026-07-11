import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

// Build the SPA to static assets that get embedded into the sandforge binary (internal/agents).
// base: "./" makes asset URLs relative so they resolve no matter what path the server mounts at.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "./",
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    outDir: "../../internal/agents/webdist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
});
