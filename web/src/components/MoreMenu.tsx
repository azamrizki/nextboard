// The header's "…" on phones and tablets (DESIGN.md §3.4): what the desktop header shows as
// separate buttons. Inside a project it also holds the pages the project header has no room for.

import { useState } from 'react';
import { useAuth } from '../auth';
import { useInstall } from '../lib/install';
import { useLayout } from '../lib/layout';
import { memoryPath, navigate } from '../lib/router';
import { setThemePref, useTheme } from '../lib/theme';
import type { Project } from '../lib/types';
import { AboutHubDialog, useBuildLabel } from './HubInfo';
import { Menu, type MenuEntry } from './Menu';

export function MoreMenu({ project, avatar }: { project?: Project; avatar?: boolean }) {
  const { me, logout, hub } = useAuth();
  const { theme } = useTheme();
  const { layout } = useLayout();
  const { state: installState, install } = useInstall();
  const [about, setAbout] = useState(false);
  const buildLabel = useBuildLabel();
  const next = theme === 'dark' ? 'light' : 'dark';

  const items = (): MenuEntry[] => [
    ...(project
      ? ([
          { label: 'Projects', icon: 'grid', onSelect: () => navigate('/') },
          { label: 'Memory', icon: 'brain', onSelect: () => navigate(memoryPath(project.id)) },
          { label: 'Machines', icon: 'server', onSelect: () => navigate('/machines') },
          { label: 'Devices', icon: 'phone', onSelect: () => navigate('/devices') },
          'sep',
        ] as MenuEntry[])
      : []),
    // a phone outside a project has Settings in its tab bar
    ...(project || layout !== 'phone' ? [{ label: 'Settings', icon: 'sliders', onSelect: () => navigate('/settings') } as MenuEntry] : []),
    // only while the browser offers it (lib/install.ts): Settings › Install app says the rest
    ...(installState === 'ready' ? [{ label: 'Install app', icon: 'download', onSelect: () => void install() } as MenuEntry] : []),
    { label: `Switch to ${next} mode`, icon: theme === 'dark' ? 'sun' : 'moon', onSelect: () => setThemePref(next) },
    // the build is here on a phone, which has no room for the header's label
    { label: 'About this hub', icon: 'info', meta: buildLabel || undefined, onSelect: () => setAbout(true) },
    'sep',
    me.kind === 'admin'
      ? { label: 'Log out', icon: 'logout', onSelect: () => void logout() }
      : { type: 'heading', label: `Signed in as ${me.deviceName}` },
  ];

  return (
    <>
      <Menu
        label="More"
        items={items}
        className="more-menu"
        // the islands bar: the hub as a round avatar (its first letter), the same menu
        trigger={avatar ? <span className="nb-avatar" aria-hidden="true">{(me?.kind === 'device' ? me.deviceName : hub?.name || 'N').trim().charAt(0).toUpperCase() || 'N'}</span> : undefined}
        triggerClassName={avatar ? 'nb-avatar-btn' : undefined}
      />
      {about && <AboutHubDialog onClose={() => setAbout(false)} />}
    </>
  );
}
