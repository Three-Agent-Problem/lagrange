/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'

// `npm run dev` proxies the API to a local `lagrange serve` (LAGRANGE_DEV_SERVER, default :8090).
const server = process.env.LAGRANGE_DEV_SERVER ?? 'http://localhost:8090'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: { '/api': server, '/a/': server, '/registry': server },
  },
  build: { chunkSizeWarningLimit: 1200 },
  test: {
    include: ['src/**/*.test.{ts,tsx}'],
    environment: 'node',
  },
})
