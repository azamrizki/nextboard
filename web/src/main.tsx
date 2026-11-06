import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
// Fonts are bundled locally (the hub's CSP only allows font-src 'self' data:).
import '@fontsource-variable/geist';
import '@fontsource-variable/geist-mono';
import '@fontsource-variable/jetbrains-mono';
import './lib/theme';
import './lib/layout';
import { installLongPress } from './lib/longpress';
import './styles/tokens.css';
import './styles/base.css';
import './styles/components.css';
import './styles/pages.css';
import './styles/chrome.css';
import './styles/agents.css';
import './styles/usage.css';
import './styles/uptime.css';
import './styles/vault.css';
import { App } from './App';

// a long press is a right-click on the board and where marked (lib/longpress.ts)
installLongPress();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
