import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Dev server proxies all /api/* (REST + the SSE exam-status stream) to the running
// backend so the host HMR build talks to the real API with no component changes and
// no CORS (the browser only ever sees the same :5173 origin).
// Target defaults to the nginx container on :80 (zero infra setup — it already proxies
// /api to the backend). Set VITE_PROXY_TARGET=http://localhost:8080 to hit the backend
// directly when it's published on the host (see docker-compose.dev.yml / local `go run`).
const proxyTarget = process.env.VITE_PROXY_TARGET || 'http://localhost'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': { target: proxyTarget, changeOrigin: true },
    },
  },
  test: {
    environment: 'happy-dom',
    globals: true,
    include: ['src/**/*.test.ts'],
  },
})
