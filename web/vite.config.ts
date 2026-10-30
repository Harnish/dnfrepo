import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { writeFileSync } from 'node:fs'

// emptyOutDir removes dist/.gitkeep, which the Go embed (and git) rely on
const keepGitkeep = () => ({
  name: 'keep-gitkeep',
  closeBundle() { writeFileSync('dist/.gitkeep', '') },
})

export default defineConfig({
  plugins: [react(), keepGitkeep()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: { proxy: { '/api': 'http://localhost:8080', '/r': 'http://localhost:8080', '/gpg.key': 'http://localhost:8080' } },
  test: { environment: 'node' },
})
