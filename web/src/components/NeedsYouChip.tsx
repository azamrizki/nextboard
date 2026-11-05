// Header chip: how many consoles need you (docs/API-agents.md §12). Click — or the
// needsYou shortcut (N) on a board — jumps to the oldest one, then the next.

import { jumpToNextNeedsYou } from '../agents/jump';
import { useShortcuts } from '../agents/settings';
import { useNeedsYou } from '../store';
import { Icon } from './Icon';

export function NeedsYouChip() {
  const list = useNeedsYou();
  const sc = useShortcuts();
  const n = list.length;
  if (n === 0) return null;
  const key = sc.needsYou.toUpperCase();
  const first = list[0];
  return (
    <button
      type="button"
      className="needs-chip"
      onClick={jumpToNextNeedsYou}
      aria-label={`${n} console${n === 1 ? ' needs' : 's need'} you. Jump to the next one (${key})`}
      title={`${first.handle ? `@${first.handle}` : first.name}${first.activity ? `: ${first.activity}` : ''} · ${key} jumps to the next`}
    >
      <Icon name="bell" />
      <span className="tabular">{n}</span>
      <span className="needs-chip-label">{n === 1 ? 'needs you' : 'need you'}</span>
      <kbd className="needs-chip-kbd" aria-hidden="true">
        {key}
      </kbd>
    </button>
  );
}
