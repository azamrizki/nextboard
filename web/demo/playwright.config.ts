import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from '@playwright/test';

// The screenshots and the product video of the README, from a hub of made-up data (setup.ts).
// `make demo` builds the hub, runs this, and turns the recording into docs/media/demo.mp4.
export const PORT = Number(process.env.NEXTBOARD_DEMO_PORT || 12396);
// what the web tile previews; README of acme-shop says 4173
export const SITE_PORT = Number(process.env.NEXTBOARD_DEMO_SITE_PORT || 4173);
// the stand-in API provider of Settings › API providers
export const ROUTER_PORT = Number(process.env.NEXTBOARD_DEMO_ROUTER_PORT || 4180);
// Shown in the UI as the projects' folder, so it should read like one.
export const ROOT = process.env.NEXTBOARD_DEMO_ROOT || '/tmp/demo';
export const STATE_FILE = `${ROOT}.state.json`;
// what was recorded, before scripts/demo-media.sh picks it over (bin/ is not in git)
export const OUT = process.env.NEXTBOARD_DEMO_OUT || resolve(dirname(fileURLToPath(import.meta.url)), '../../bin/demo-out');

export default defineConfig({
  testDir: '.',
  outputDir: `${OUT}/results`,
  globalSetup: './setup.ts',
  workers: 1,
  timeout: 300_000,
  reporter: 'list',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    storageState: STATE_FILE,
    viewport: { width: 1600, height: 900 },
    colorScheme: 'dark',
  },
  projects: [{ name: 'demo' }],
});
