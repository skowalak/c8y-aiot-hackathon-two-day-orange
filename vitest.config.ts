import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    include: ['test/**/*.test.ts'],
    environment: 'node',
    // Tests share the in-memory session store singleton; keep files isolated
    // but run sequentially within a file (default) to avoid cross-talk.
    globals: false,
  },
})
