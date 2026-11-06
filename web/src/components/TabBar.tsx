// Phones, outside a project: the pages along the bottom edge, in reach of a thumb (DESIGN.md
// §3.4). It gives way to the on-screen keyboard.

import { Link, parseRoute, usePath } from '../lib/router';
import { Icon, type IconName } from './Icon';

const TABS: { to: string; label: string; icon: IconName; page: string }[] = [
  { to: '/', label: 'Projects', icon: 'grid', page: 'home' },
  { to: '/memory', label: 'Memory', icon: 'brain', page: 'memory' },
  { to: '/machines', label: 'Machines', icon: 'server', page: 'machines' },
  { to: '/devices', label: 'Devices', icon: 'phone', page: 'devices' },
  { to: '/settings', label: 'Settings', icon: 'sliders', page: 'settings' },
];

export function TabBar() {
  const page = parseRoute(usePath()).page;
  return (
    <nav className="tab-bar" aria-label="Main">
      {TABS.map((t) => (
        <Link key={t.to} to={t.to} className="tab-bar-item" aria-current={page === t.page ? 'page' : undefined}>
          <span className="tab-bar-icon">
            <Icon name={t.icon} />
          </span>
          <span className="tab-bar-label">{t.label}</span>
        </Link>
      ))}
    </nav>
  );
}
