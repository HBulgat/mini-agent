import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import path from 'node:path';

// Vite + Vitest config for mini-agent's Web UI.
//
// We import `defineConfig` from `vitest/config` (a thin re-export of
// vite's) so the `test` field is recognized — vite's own `defineConfig`
// doesn't know about it and trips a TS overload error.
//
//   - Dev server proxies /api → http://127.0.0.1:7777 (the gin backend
//     started by `mini-agent serve`). The proxy keeps the SSE upgrade
//     intact via `ws: false, changeOrigin: true`.
//   - `@/...` resolves to `src/` so deeply-nested components don't pile
//     up `../../../` chains.
//   - Build output goes to `dist/`; static asset hashing is on by default.
//
// Vitest-only knobs:
//   - antd v5's ESM build uses bare directory imports (e.g.
//     `from 'antd/es/version'`) that Node's native ESM resolver refuses.
//     `server.deps.inline` forces Vitest to transform antd & its rc-*
//     peers through Vite's resolver, which handles the package's
//     `exports` map correctly. The production bundle is unaffected.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:7777',
        changeOrigin: true,
        // SSE works over plain HTTP; no ws upgrade needed.
        ws: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    target: 'es2022',
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    // Note on antd v5 + Vitest:
    //   antd's ESM build uses bare directory imports like
    //   `from 'antd/es/version'` that Node's native ESM resolver refuses,
    //   and Vitest 2.1's `server.deps.inline` does not reliably override
    //   that resolution path under JSDOM. Until Vitest fully bundles SSR
    //   imports through Vite, individual test files mock antd /
    //   @ant-design/x with `vi.mock(...)` (see src/__tests__/App.test.tsx).
    //   Real visual rendering of AntD components is exercised in T6.7.
    //
    // We still set the inline list defensively so tests that DO want
    // real antd (e.g. via importing a non-component sub-utility) get
    // it routed through Vite's resolver.
    server: {
      deps: {
        inline: [/^antd/, /^@ant-design\//, /^rc-/],
      },
    },
  },
});
