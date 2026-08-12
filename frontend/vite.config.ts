import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // ws:true matters: the job stream lives at /api/v1/submissions/:id/stream,
      // so it is matched by THIS rule. Without ws the upgrade fails and the UI
      // silently falls back to polling. The old config put ws:true on a '/ws'
      // prefix that no backend route uses.
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
