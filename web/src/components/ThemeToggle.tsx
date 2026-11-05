// The header's quick switch between light and dark. Following the system again is in
// Settings › Appearance.

import { setThemePref, useTheme } from '../lib/theme';
import { Icon } from './Icon';

export function ThemeToggle() {
  const { theme } = useTheme();
  const next = theme === 'dark' ? 'light' : 'dark';
  const label = `Switch to ${next} mode`;
  return (
    <button type="button" className="btn btn-ghost btn-sm btn-icon theme-toggle" aria-label={label} title={label} onClick={() => setThemePref(next)}>
      <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
    </button>
  );
}
