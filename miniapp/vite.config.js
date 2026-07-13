import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'

export default defineConfig({
  plugins: [preact()],
  base: '/miniapp/',
  build: {
    outDir: '../internal/backend/miniapp_static',
    emptyOutDir: true,
  },
})
