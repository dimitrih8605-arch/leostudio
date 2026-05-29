import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Vite config for the Wails desktop frontend.
// `@/` resolves to ./src so feature pages can import from common locations.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    // Wails embeds the dist directory; sourcemaps stay off for smaller bundles.
    sourcemap: false,
  },
});
