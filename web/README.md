# mini-agent Web UI

React 18 + Vite + TypeScript + Ant Design v5 + `@ant-design/x` —
the front-end half of mini-agent (`mini-agent serve` boots the gin
backend; this app talks to it over REST + SSE).

## Quick start

```bash
# from repo root
make web-install   # one-time `pnpm install`
make web-dev       # vite dev server on http://localhost:5173
make web-build     # production bundle to web/dist
make web-test      # vitest run

# or directly
cd web
pnpm install
pnpm dev
```

The dev server proxies `/api/*` to `http://127.0.0.1:7777` (the default
bind for `mini-agent serve`). Adjust `server.proxy` in `vite.config.ts`
if your backend listens elsewhere.

## Layout (per `docs/system-design/01-overall-architecture.md` §1.2)

```
src/
├── main.tsx           # Vite entry; renders <App />
├── App.tsx            # AntD ConfigProvider + router (router lands in T5.5)
├── routes/            # react-router route definitions       (T5.5+)
├── pages/             # page-level components                (T5.5+)
├── components/        # message bubble, tool card, diff …    (T5.6+)
├── stores/            # zustand stores                       (T5.6+)
├── api/               # axios + react-query hooks            (T5.1+)
├── hooks/             # useSSE etc.                          (T5.2+)
├── types/             # shared TS types
└── test/              # test setup + helpers
```

## Status

This is the T0.7 skeleton — only the build system and the bare AntD +
`@ant-design/x` greeter render. Real pages, routes, stores, API hooks
land in Iter-5 / Iter-6 (T5.* / T6.*).

## Tech stack notes

- **Package manager**: pnpm 9, activated via Corepack (Node 20+ ships
  it). The repo pins `packageManager` in `package.json` so every
  contributor sees the same version.
- **Registry**: a project-local `.npmrc` mirrors the registry to
  `registry.npmmirror.com` for environments that can't reach
  registry.npmjs.org directly. Override with `pnpm config set registry
  ...` if you don't need the mirror.
- **Test runner**: Vitest with the JSDOM environment. The setup file at
  `src/test/setup.ts` wires in `@testing-library/jest-dom` matchers.
