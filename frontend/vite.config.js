import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// The build lands inside internal/web/dist so the Go binary can embed it.
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:7788', changeOrigin: true, ws: false },
    },
  },
});
