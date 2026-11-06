// The app's header, one per layout (DESIGN.md §3.4).
//
//   desktop  floating glass bar: brand + hub chip, navigation, usage, needs-you and live chips,
//            connection, then the vault key, Settings, theme, About and Log out as buttons
//   tablet   the same bar on one row, with Settings, theme, About and Log out (and, inside a
//            project, the other pages) in a "…" menu
//   phone    a bar along the top edge, one row, and outside a project a tab bar along the bottom
//
// Inside a project the navigation gives way to the project switcher and the workspace tabs (a
// phone has one switcher for both). The key button opens the vault on any page, with no
// Credentials card needed.

import { lazy, Suspense, useState } from 'react';
import { useAuth } from '../auth';
import { useLayout } from '../lib/layout';
import { Link, memoryPath, parseRoute, usePath } from '../lib/router';
import type { Project } from '../lib/types';
import { useEventsConnected, useStoreReady } from '../store';
import { Button } from './Button';
import { HubInfoButton } from './HubInfo';
import { ThemeToggle } from './ThemeToggle';
import { Icon, type IconName } from './Icon';
import { LiveChip } from './LiveChip';
import { MoreMenu } from './MoreMenu';
import { NeedsYouChip } from './NeedsYouChip';
import { PhoneSwitcher, ProjectSwitcher, WorkspaceTabs } from './ProjectBar';
import { TabBar } from './TabBar';
import { UsagePill } from '../usage/UsagePill';
import { useVaultStatus } from '../vault/store';

// The vault's screens load, and the vault with them, when the window is first opened.
const VaultWindow = lazy(() => import('../vault/Window').then((m) => ({ default: m.VaultWindow })));

const NAV: { to: string; label: string; icon: IconName; page: string }[] = [
  { to: '/', label: 'Projects', icon: 'grid', page: 'home' },
  { to: '/memory', label: 'Memory', icon: 'brain', page: 'memory' },
  { to: '/machines', label: 'Machines', icon: 'server', page: 'machines' },
  { to: '/devices', label: 'Devices', icon: 'phone', page: 'devices' },
];

export function BrandMark({ large }: { large?: boolean }) {
  return (
    <span className={`brand-mark ${large ? 'lg' : ''}`} aria-hidden="true">
      <Icon name="terminal" />
    </span>
  );
}

interface AppHeaderProps {
  /** Inside a project board: show the project switcher and workspace tabs. */
  project?: Project;
  workspaceId?: string;
}

function ConnStatus() {
  const connected = useEventsConnected();
  const ready = useStoreReady();
  const connLabel = connected ? 'Live' : ready ? 'Reconnecting…' : 'Connecting…';
  return (
    <span
      className={`status conn ${connected ? 'is-live' : ''}`}
      role="status"
      aria-live="polite"
      title={connected ? 'Live updates connected' : 'Live updates disconnected — reconnecting'}
    >
      <span className={`dot ${connected ? 'dot-success' : 'dot-warning'}`} aria-hidden="true" />
      <span className={`conn-text ${connected ? 'conn-label-live' : 'text-warning'}`}>{connLabel}</span>
    </span>
  );
}

function VaultButton() {
  const unlocked = useVaultStatus() === 'unlocked';
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        iconOnly
        icon="key"
        className={`vault-button ${unlocked ? 'is-unlocked' : ''}`}
        aria-label={unlocked ? 'Credentials, unlocked' : 'Credentials'}
        title={unlocked ? 'Credentials (unlocked)' : 'Credentials'}
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
      />
      {open && (
        <Suspense fallback={null}>
          <VaultWindow onClose={() => setOpen(false)} />
        </Suspense>
      )}
    </>
  );
}

function PhoneHeader({ project, workspaceId }: AppHeaderProps) {
  return (
    <>
      <header className={`app-header phone-bar ${project ? 'in-project' : ''}`}>
        <div className="brand">
          {project ? (
            <>
              <Link to="/" className="brand-link" aria-label="All projects" title="All projects">
                <BrandMark />
              </Link>
              <PhoneSwitcher project={project} activeId={workspaceId} />
            </>
          ) : (
            <>
              <BrandMark />
              <span className="brand-name">NextBoard</span>
            </>
          )}
        </div>
        <div className="header-right">
          <UsagePill machineId={project?.machineId ?? 'local'} />
          <NeedsYouChip />
          <LiveChip />
          <ConnStatus />
          <VaultButton />
          <MoreMenu project={project} />
        </div>
      </header>
      {!project && <TabBar />}
    </>
  );
}

export function AppHeader({ project, workspaceId }: AppHeaderProps) {
  const { hub, me, logout } = useAuth();
  const { layout } = useLayout();
  const page = parseRoute(usePath()).page;
  const inProject = !!project;

  if (layout === 'phone') return <PhoneHeader project={project} workspaceId={workspaceId} />;
  const desktop = layout === 'desktop';

  return (
    <header className={`app-header ${inProject ? 'in-project' : ''}`}>
      <div className="brand">
        {inProject ? (
          <Link to="/" className="brand-link" aria-label="All projects" title="All projects">
            <BrandMark />
          </Link>
        ) : (
          <>
            <BrandMark />
            <span className="brand-name">NextBoard</span>
            {hub && (
              <span className="badge hub-chip truncate" title={`Hub: ${hub.name}`}>
                {hub.name}
              </span>
            )}
          </>
        )}
        {project && <ProjectSwitcher project={project} />}
      </div>

      {project ? (
        <nav className="app-tabs" aria-label="Workspaces">
          <WorkspaceTabs project={project} activeId={workspaceId} />
        </nav>
      ) : (
        <nav className="app-nav" aria-label="Main">
          <div className="segmented">
            {NAV.map((n) => (
              <Link key={n.to} to={n.to} className="segmented-item" aria-current={page === n.page ? 'page' : undefined} title={n.label}>
                <Icon name={n.icon} />
                <span className="nav-label">{n.label}</span>
              </Link>
            ))}
          </div>
        </nav>
      )}

      <div className="header-right">
        <UsagePill machineId={project?.machineId ?? 'local'} />
        <NeedsYouChip />
        <LiveChip />
        <ConnStatus />
        <div className="header-actions">
          {inProject && desktop && (
            <nav className="icon-nav" aria-label="Main">
              {NAV.slice(1).map((n) => (
                <Link key={n.to} to={n.page === 'memory' ? memoryPath(project?.id) : n.to} className="btn btn-ghost btn-sm btn-icon" aria-label={n.label} title={n.label}>
                  <Icon name={n.icon} />
                </Link>
              ))}
            </nav>
          )}
          <VaultButton />
          {desktop ? (
            <>
              <Link
                to="/settings"
                className="btn btn-ghost btn-sm btn-icon settings-link"
                aria-label="Settings"
                title="Settings"
                aria-current={page === 'settings' ? 'page' : undefined}
              >
                <Icon name="sliders" />
              </Link>
              <ThemeToggle />
              <HubInfoButton />
              {me.kind === 'admin' ? (
                <Button variant="ghost" size="sm" icon="logout" className="logout-btn" onClick={() => void logout()} aria-label="Log out" title="Log out">
                  <span className="logout-label">Log out</span>
                </Button>
              ) : (
                <span className="device-name truncate" title="Signed in as this paired device">
                  {me.deviceName}
                </span>
              )}
            </>
          ) : (
            <MoreMenu project={project} />
          )}
        </div>
      </div>
    </header>
  );
}
