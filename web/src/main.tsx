import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { ConfigProvider, App as AntdApp } from 'antd';

import App from './App';
import './styles/global.css';

// Vite entry point. We wrap the tree in:
//   - StrictMode               surfaces sloppy lifecycle usage in dev
//   - ConfigProvider           AntD theme/locale root (single source of truth)
//   - AntdApp                  unlocks AntD's static `message`/`notification`
//                              APIs (vs. the legacy global Message)
//
// Routing, react-query QueryClientProvider, and Zustand stores will be
// layered in inside <App /> as their respective tasks land (T5.1+).

const container = document.getElementById('root');
if (!container) {
  throw new Error('mini-agent: #root element missing from index.html');
}

createRoot(container).render(
  <StrictMode>
    <ConfigProvider
      theme={{
        cssVar: true,
        token: {
          // Match Claude Code's vibe — neutral, slightly warm dark accent.
          colorPrimary: '#1f6feb',
          borderRadius: 6,
        },
      }}
    >
      <AntdApp>
        <App />
      </AntdApp>
    </ConfigProvider>
  </StrictMode>,
);
