// Boot flow + routing.
//
//   GET /api/setup/status → needsSetup → /setup
//   else GET /api/auth/me  → 401 → /login, otherwise the app:
//   /  (Home: projects) · /p/:projectId/w/:workspaceId (board) · /machines · /devices

import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError, errorMessage, isStatus, setUnauthorizedHandler } from './api';
import { startAttention } from './agents/attention';
import { loadSettings } from './agents/settings';
import { AuthProvider } from './auth';
import { Button } from './components/Button';
import { Icon } from './components/Icon';
import { Toaster } from './components/Toast';
import { navigate, parseRoute, usePath } from './lib/router';
import type { Me } from './lib/types';
import { DevicesPage } from './pages/Devices';
import { HomePage } from './pages/Home';
import { LoginPage } from './pages/Login';
import { MachinesPage } from './pages/Machines';
import { SetupPage } from './pages/Setup';
import { store } from './store';
import { installMock, mockRequested } from './orch/api';
import { orchStore } from './orch/store';
import { studioStore } from './studio/store';
import { guiStore } from './gui/store';
import { resetVaultStore } from './vault/store';
import { installChime } from './usage/chime';
import { installTts } from './voice/tts';

type Boot =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'setup' }
  | { kind: 'login'; hubName: string }
  | { kind: 'authed'; me: Me };

// The board (React Flow + xterm + tiles) is the heaviest part of the app: load it on demand.
const BoardPage = lazy(() => import('./pages/Board').then((m) => ({ default: m.BoardPage })));
const MemoryPage = lazy(() => import('./memory/MemoryPage').then((m) => ({ default: m.MemoryPage })));
const SettingsPage = lazy(() => import('./pages/Settings').then((m) => ({ default: m.SettingsPage })));
const PrintPage = lazy(() => import('./studio/print/PrintPage'));
const CredentialRequests = lazy(() => import('./vault/Requests').then((m) => ({ default: m.CredentialRequests })));
const PasskeyRequests = lazy(() => import('./vault/PasskeyRequests').then((m) => ({ default: m.PasskeyRequests })));

const isAppPath = (p: string) => parseRoute(p).page !== 'unknown';

function PageLoading() {
  return (
    <div className="center-msg full" role="status">
      <span className="spinner" aria-hidden="true" />
      Loading…
    </div>
  );
}

export function App() {
  const path = usePath();
  const [boot, setBoot] = useState<Boot>({ kind: 'loading' });
  // Page the user asked for before being sent to /login; restored after login.
  const returnTo = useRef(
    isAppPath(window.location.pathname) ? window.location.pathname + window.location.search : '/',
  );

  const runBoot = useCallback(async () => {
    try {
      const st = await api.setupStatus();
      if (st.needsSetup) {
        setBoot({ kind: 'setup' });
        return;
      }
      try {
        const me = await api.me({ noRedirect: true });
        setBoot({ kind: 'authed', me });
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          setBoot({ kind: 'login', hubName: st.hubName });
          return;
        }
        throw err;
      }
    } catch (err) {
      setBoot({ kind: 'error', message: errorMessage(err) });
    }
  }, []);

  useEffect(() => {
    void runBoot();
  }, [runBoot]);

  // Any 401 while using the app (expired cookie, revoked device) → login screen.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      store.reset();
      setBoot((b) => (b.kind === 'login' ? b : { kind: 'login', hubName: '' }));
      void runBoot(); // re-check (fills in the hub name for the login screen)
    });
  }, [runBoot]);

  // Keep the URL consistent with the boot state.
  useEffect(() => {
    if (boot.kind === 'setup' && path !== '/setup') navigate('/setup' + window.location.search, true);
    else if (boot.kind === 'login' && path !== '/login') {
      if (isAppPath(path)) returnTo.current = path + window.location.search;
      navigate('/login', true);
    } else if (boot.kind === 'authed' && !isAppPath(path)) {
      navigate(returnTo.current, true);
      returnTo.current = '/';
    }
  }, [boot.kind, path]);

  // Live events only while authenticated.
  useEffect(() => {
    if (boot.kind !== 'authed') return;
    // dev flag (localStorage nb.orchMock / ?orchmock=1): Phase 6 endpoints served in-browser
    if (mockRequested()) void installMock();
    orchStore.install();
    studioStore.install();
    guiStore.install();
    store.connect();
    const stopChime = installChime();
    const stopTts = installTts();
    void loadSettings();
    api
      .profiles()
      .then((list) => store.seedProfiles(list))
      .catch((err) => isStatus(err, 404) && store.seedProfiles([]));
    const stopAttention = startAttention();
    return () => {
      stopAttention();
      stopChime();
      stopTts();
      store.reset();
      orchStore.reset();
      studioStore.reset();
      guiStore.reset();
      resetVaultStore();
    };
  }, [boot.kind]);

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } catch {
      /* the session may already be gone */
    }
    store.reset();
    setBoot({ kind: 'login', hubName: '' });
    void runBoot();
  }, [runBoot]);

  return (
    <>
      {renderBoot()}
      <Toaster />
    </>
  );

  function renderBoot() {
    switch (boot.kind) {
      case 'loading':
        return (
          <div className="center-msg full" role="status">
            <span className="spinner" aria-hidden="true" />
            Connecting to your hub…
          </div>
        );
      case 'error':
        return (
          <div className="center-msg full">
            <div className="auth-card">
              <div className="empty-state">
                <span className="empty-icon">
                  <Icon name="alertCircle" />
                </span>
                <h2>Can’t reach the hub</h2>
                <p>{boot.message}. Check that the NextBoard service is running, then try again.</p>
                <Button variant="primary" icon="refresh" onClick={() => void runBoot()}>
                  Retry connection
                </Button>
              </div>
            </div>
          </div>
        );
      case 'setup':
        return <SetupPage onDone={() => void runBoot()} />;
      case 'login':
        return <LoginPage hubName={boot.hubName} onDone={() => void runBoot()} />;
      case 'authed': {
        const route = parseRoute(path);
        return (
          <AuthProvider me={boot.me} onLogout={logout}>
            {route.page === 'print' ? (
              <Suspense fallback={<PageLoading />}>
                <PrintPage kind={route.kind} docId={route.docId} versionId={route.versionId} />
              </Suspense>
            ) : route.page === 'machines' ? (
              <MachinesPage />
            ) : route.page === 'devices' ? (
              <DevicesPage />
            ) : route.page === 'memory' ? (
              <Suspense fallback={<PageLoading />}>
                <MemoryPage projectId={route.projectId} />
              </Suspense>
            ) : route.page === 'settings' ? (
              <Suspense fallback={<PageLoading />}>
                <SettingsPage pane={route.pane} />
              </Suspense>
            ) : route.page === 'board' ? (
              <Suspense fallback={<PageLoading />}>
                <BoardPage projectId={route.projectId} workspaceId={route.workspaceId} />
              </Suspense>
            ) : (
              <HomePage />
            )}
            {route.page !== 'print' && (
              <Suspense fallback={null}>
                <CredentialRequests />
                <PasskeyRequests />
              </Suspense>
            )}
          </AuthProvider>
        );
      }
    }
  }
}
