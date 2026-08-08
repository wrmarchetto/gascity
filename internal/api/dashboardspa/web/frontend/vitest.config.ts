import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// Vitest config kept separate from vite.config.ts so the dev server
// stays focused on serving the app. Tests run in jsdom; localStorage
// is the only DOM API our hooks touch directly.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [
      {
        // Point the shared package's ROOT entry at its SOURCE, not its build
        // output. node_modules/gas-city-dashboard-shared symlinks to ../shared,
        // whose "main" is dist/index.js, so without this the suite exercises
        // whatever was last compiled: a shared/src edit with no rebuild runs
        // green against stale code. Measured -- disabling the rolled-window
        // check in shared/src left all Accounts tests passing. CI only avoids
        // this incidentally, because `npm run typecheck` happens to build
        // shared before the vitest step; nobody maintains that ordering
        // deliberately.
        //
        // Anchored regex, NOT the bare string key. A string alias matches by
        // PREFIX, which rewrites the package's other export subpaths
        // (gas-city-dashboard-shared/gc-supervisor, /fixtures/test-city) into
        // paths under index.ts and breaks 25 test files at import time. Those
        // subpaths must keep resolving through the package exports.
        find: /^gas-city-dashboard-shared$/,
        replacement: new URL('../shared/src/index.ts', import.meta.url).pathname,
      },
    ],
  },
  test: {
    environment: 'jsdom',
    environmentOptions: {
      jsdom: {
        url: 'http://127.0.0.1/',
      },
    },
    include: ['src/**/*.test.{ts,tsx}'],
    globals: false,
    setupFiles: ['src/test/setup.ts'],
  },
});
