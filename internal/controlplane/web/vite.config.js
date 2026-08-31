import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { assetsInlineLimit: 0 },
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
});
