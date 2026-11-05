import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The Go hub embeds ../internal/webui/dist via go:embed, so the build lands there.
// In dev, /api (HTTP + WebSocket) is proxied to a hub started with `nextboard serve --dev`.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // monaco-vim imports an older Monaco's layout, which this Monaco's exports map turns away
      'monaco-editor/esm/vs/editor/editor.api': 'monaco-editor/editor/editor.api.js',
    },
  },
  // Found late, these would make the dev server reload the page in the middle of unlocking the
  // vault: they only load in a worker, a dialog, or on plain HTTP (src/vault).
  optimizeDeps: {
    include: ['hash-wasm', 'zxcvbn', '@scure/bip39/wordlists/english.js', '@noble/ciphers/aes.js', '@noble/hashes/hmac.js', '@noble/hashes/legacy.js', '@noble/hashes/sha2.js'],
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
    // gui-viewer.html is the page inside desktop app tiles (src/gui/viewer.ts).
    rolldownOptions: {
      input: ['index.html', 'gui-viewer.html'],
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': {
        target: process.env.NEXTBOARD_HUB ?? 'http://localhost:12312',
        ws: true,
      },
    },
  },
});
