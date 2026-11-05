import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

// Browser tests of what unit tests cannot see: a layout that breaks, a drag that does nothing.
// They run against a throwaway hub with fake agents, on its own port and in its own folder,
// and never start a real agent CLI. `make test-e2e` builds the hub they need.
export const PORT = Number(process.env.NEXTBOARD_E2E_PORT || 12398);
export const STATE_FILE = join(tmpdir(), `nextboard-e2e-state-${PORT}.json`);

export default defineConfig({
  testDir: '.',
  outputDir: join(tmpdir(), `nextboard-e2e-results-${PORT}`),
  globalSetup: './global-setup.ts',
  workers: 1,
  timeout: 45_000,
  reporter: 'list',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    storageState: STATE_FILE,
    viewport: { width: 1500, height: 950 },
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'light', use: { colorScheme: 'light' } },
    { name: 'dark', use: { colorScheme: 'dark' } },
  ],
});
