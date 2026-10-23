import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物直接落到 server/web，供 Go 的 go:embed 内嵌。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../server/web',
    emptyOutDir: true
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8088'
    }
  }
})
