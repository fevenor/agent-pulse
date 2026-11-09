import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:18700", ws: true },
      "/healthz": { target: "http://127.0.0.1:18700" },
    },
  },
});
