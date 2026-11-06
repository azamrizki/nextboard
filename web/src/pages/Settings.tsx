// Settings (docs/API-agents.md §11, §17): hub-wide settings (agents, profiles,
// notifications, shortcuts — shared by every device) and per-browser preferences
// (appearance, whether this browser shows notifications, installing the app). A phone shows the list of panes at
// /settings and one pane at a time, with a way back; the other layouts show both side by side.

import { useEffect, useState } from 'react';
import { loadSettings, settingsSupported } from '../agents/settings';
import { useLayout } from '../lib/layout';
import { AppHeader } from '../components/AppHeader';
import { Icon, type IconName } from '../components/Icon';
import { Link } from '../lib/router';
import { AgentsPane } from '../settings/AgentsPane';
import { AppearancePane } from '../settings/AppearancePane';
import { AppPane } from '../settings/AppPane';
import { McpPane } from '../settings/McpPane';
import { NotificationsPane } from '../settings/NotificationsPane';
import { ProfilesPane } from '../settings/ProfilesPane';
import { ShortcutsPane } from '../settings/ShortcutsPane';
import { ConnectionsPane } from '../settings/ConnectionsPane';
import { StudiosPane } from '../settings/StudiosPane';
import { LeadPane } from '../settings/LeadPane';
import { UsagePane } from '../settings/UsagePane';
import { VoicePane } from '../settings/VoicePane';
import { useSettings } from '../store';

const PANES: { id: string; label: string; icon: IconName; hint: string }[] = [
  { id: 'agents', label: 'Agents', icon: 'bot', hint: 'Catalog, defaults, your agents' },
  { id: 'lead', label: 'Lead agent', icon: 'orchestrator', hint: 'Its name, how it talks and leads' },
  { id: 'profiles', label: 'Login profiles', icon: 'user', hint: 'Several accounts per CLI' },
  { id: 'mcp', label: 'MCP servers', icon: 'cpu', hint: 'Tools every agent gets' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', hint: 'When an agent needs you' },
  { id: 'shortcuts', label: 'Shortcuts', icon: 'keyboard', hint: 'Single-key board tools' },
  { id: 'usage', label: 'Usage', icon: 'gauge', hint: 'Usage checks and the reset chime' },
  { id: 'studios', label: 'Previews & studios', icon: 'globe', hint: 'Web previews, hidden runs, Git' },
  { id: 'voice', label: 'Voice', icon: 'mic', hint: 'Push-to-talk, dictation, this browser' },
  { id: 'connections', label: 'Connections', icon: 'plug', hint: 'GitHub CLI per machine' },
  { id: 'appearance', label: 'Appearance', icon: 'textSize', hint: 'This browser only' },
  { id: 'app', label: 'Install app', icon: 'download', hint: 'Home screen, this device' },
];

// panes kept by this browser, which an older hub does not change
const LOCAL = new Set(['appearance', 'voice', 'app']);

export function SettingsPage({ pane }: { pane: string }) {
  const settings = useSettings();
  const [checked, setChecked] = useState(settingsSupported() !== null);
  const phone = useLayout().layout === 'phone';
  const current = PANES.find((p) => p.id === pane) ?? PANES[0];
  const listOnly = phone && !PANES.some((p) => p.id === pane);

  useEffect(() => {
    if (!settings) void loadSettings().finally(() => setChecked(true));
  }, [settings]);

  useEffect(() => {
    document.title = listOnly ? 'Settings — NextBoard' : `${current.label} · Settings — NextBoard`;
    return () => {
      document.title = 'NextBoard';
    };
  }, [current.label, listOnly]);

  const unsupported = checked && settingsSupported() === false;

  return (
    <div className="page">
      <AppHeader />
      <main className="content settings-page">
        {phone && !listOnly ? (
          <Link to="/settings" className="settings-back">
            <Icon name="chevronLeft" />
            Settings
          </Link>
        ) : (
          <div className="page-head">
            <div>
              <h1>Settings</h1>
              <p>Agent, notification and shortcut settings are stored on the hub and apply to every device.</p>
            </div>
          </div>
        )}
        <div className={`settings-layout ${phone ? (listOnly ? 'is-list' : 'is-pane') : ''}`}>
          <nav className="settings-nav" aria-label="Settings sections">
            {PANES.map((p) => (
              <Link key={p.id} to={`/settings/${p.id}`} className="settings-nav-item" aria-current={!listOnly && p.id === current.id ? 'page' : undefined}>
                <Icon name={p.icon} />
                <span className="settings-nav-text">
                  <span className="settings-nav-label">{p.label}</span>
                  <span className="settings-nav-hint">{p.hint}</span>
                </span>
                {phone && <Icon name="chevronRight" className="settings-nav-chev" />}
              </Link>
            ))}
          </nav>
          {!listOnly && (
          <section className="settings-pane" aria-labelledby="settings-pane-title">
            <h2 id="settings-pane-title" className="settings-pane-title">
              <Icon name={current.icon} />
              {current.label}
            </h2>
            {unsupported && !LOCAL.has(current.id) && (
              <div className="alert warning" role="status">
                <Icon name="alert" />
                <span>This hub doesn’t support agent settings yet. Update NextBoard on the hub, then reload this page.</span>
              </div>
            )}
            {current.id === 'agents' && <AgentsPane />}
            {current.id === 'profiles' && <ProfilesPane />}
            {current.id === 'mcp' && <McpPane />}
            {current.id === 'notifications' && <NotificationsPane />}
            {current.id === 'shortcuts' && <ShortcutsPane />}
            {current.id === 'lead' && <LeadPane />}
            {current.id === 'usage' && <UsagePane />}
            {current.id === 'studios' && <StudiosPane />}
            {current.id === 'voice' && <VoicePane />}
            {current.id === 'connections' && <ConnectionsPane />}
            {current.id === 'appearance' && <AppearancePane />}
            {current.id === 'app' && <AppPane />}
          </section>
          )}
        </div>
      </main>
    </div>
  );
}
