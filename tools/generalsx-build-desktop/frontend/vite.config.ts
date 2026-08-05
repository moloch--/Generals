import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import {writeFileSync} from "node:fs";
import {fileURLToPath} from "node:url";
import {defineConfig} from "vitest/config";

export default defineConfig({
  base: "./",
  plugins: [
    react(),
    tailwindcss(),
    {
      name: "generalsx-embed-placeholder",
      closeBundle() {
        // GeneralsX @build Codex 05/08/2026 Keep a tracked embed target after Vite empties dist.
        writeFileSync(fileURLToPath(new URL("./dist/gitkeep", import.meta.url)), "");
      },
    },
  ],
  build: {
    emptyOutDir: true,
    outDir: "dist",
    sourcemap: false,
  },
  test: {
    environment: "node",
  },
});
