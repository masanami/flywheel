/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  root: fileURLToPath(new URL(".", import.meta.url)),
  plugins: [react()],
  server: {
    // 手元の待ち受けだけ（127.0.0.1 固定）。外から上書きする口は作らない。
    host: "127.0.0.1",
    proxy: {
      "/api": { target: "http://127.0.0.1:4318", changeOrigin: true },
    },
  },
  build: {
    // go:embed が読む場所。internal/server/dist/ は git の追跡外（.gitkeep だけ追跡）。
    // 掃除するのは ui/ の中だけで、追跡している .gitkeep には触れない。
    outDir: fileURLToPath(
      new URL("../internal/server/dist/ui", import.meta.url),
    ),
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    setupFiles: ["src/test-setup.ts"],
  },
});
