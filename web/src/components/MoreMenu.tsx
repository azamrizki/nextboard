// The header's "…" on phones and tablets (DESIGN.md §3.4): what the desktop header shows as
// separate buttons. Inside a project it also holds the pages the project header has no room for.

import { useState } from 'react';
import { useAuth } from '../auth';
import { useLayout } from '../lib/layout';
import { memoryPath, navigate } from '../lib/router';
import { setThemePref, useTheme } from '../lib/theme';
import type { Project } from '../lib/types';
import { AboutHubDialog } from './HubInfo';
import { Menu, type MenuEntry } from './Menu';

export function MoreMenu({ project }: { project?: Project }) {
  const { me, logout } = useAuth();
  const { theme } = useTheme();
  const { layout } = useLayout();
  const [about, setAbout] = useState(false);
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
    { label: `Switch to ${next} mode`, icon: theme === 'dark' ? 'sun' : 'moon', onSelect: () => setThemePref(next) },
    { label: 'About this hub', icon: 'info', onSelect: () => setAbout(true) },
    'sep',
    me.kind === 'admin'
      ? { label: 'Log out', icon: 'logout', onSelect: () => void logout() }
      : { type: 'heading', label: `Signed in as ${me.deviceName}` },
  ];

  return (
    <>
      <Menu label="More" items={items} className="more-menu" />
      {about && <AboutHubDialog onClose={() => setAbout(false)} />}
    </>
  );
}
