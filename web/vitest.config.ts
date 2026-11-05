import { defineConfig } from 'vitest/config';

// Unit tests of what must not break quietly. A file that needs a DOM says so with
// `// @vitest-environment jsdom` on its first line.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    testTimeout: 60_000,
  },
});
