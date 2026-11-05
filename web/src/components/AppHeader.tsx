// Floating glass header (DESIGN.md §3.4): brand + hub chip, navigation, live chip with
// running terminals, connection indicator, About popover, Log out. Inside a project the
// navigation gives way to the project switcher and the workspace tabs.

import { useAuth } from '../auth';
import { Link, memoryPath, parseRoute, usePath } from '../lib/router';
import type { Project } from '../lib/types';
import { useEventsConnected, useStoreReady } from '../store';
import { Button } from './Button';
import { HubInfoButton } from './HubInfo';
import { ThemeToggle } from './ThemeToggle';
import { Icon, type IconName } from './Icon';
import { LiveChip } from './LiveChip';
import { NeedsYouChip } from './NeedsYouChip';
import { ProjectSwitcher, WorkspaceTabs } from './ProjectBar';
import { UsagePill } from '../usage/UsagePill';

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

export function AppHeader({ project, workspaceId }: AppHeaderProps) {
  const { hub, me, logout } = useAuth();
  const path = usePath();
  const page = parseRoute(path).page;
  const connected = useEventsConnected();
  const ready = useStoreReady();
  const connLabel = connected ? 'Live' : ready ? 'Reconnecting…' : 'Connecting…';
  const inProject = !!project;

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
              <Link key={n.to} to={n.to} className="segmented-item" aria-current={page === n.page ? 'page' : undefined}>
                <Icon name={n.icon} />
                {n.label}
              </Link>
            ))}
          </div>
        </nav>
      )}

      <div className="header-right">
        <UsagePill machineId={project?.machineId ?? 'local'} />
        <NeedsYouChip />
        <LiveChip />
        <span
          className="status conn"
          role="status"
          aria-live="polite"
          title={connected ? 'Live updates connected' : 'Live updates disconnected — reconnecting'}
        >
          <span className={`dot ${connected ? 'dot-success' : 'dot-warning'}`} aria-hidden="true" />
          <span className={`conn-text ${connected ? 'conn-label-live' : 'text-warning'}`}>{connLabel}</span>
        </span>
        <div className="header-actions">
          {inProject && (
            <nav className="icon-nav" aria-label="Main">
              {NAV.slice(1).map((n) => (
                <Link key={n.to} to={n.page === 'memory' ? memoryPath(project?.id) : n.to} className="btn btn-ghost btn-sm btn-icon" aria-label={n.label} title={n.label}>
                  <Icon name={n.icon} />
                </Link>
              ))}
            </nav>
          )}
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
        </div>
      </div>
    </header>
  );
}
