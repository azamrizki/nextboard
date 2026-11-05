// The display button of a card's header (DESIGN.md §3.5): Fit tile, three monitors, five phones.
// A web tile always shows it; other cards show it on hover, or while they have one of the sizes.

import { Menu } from '../components/Menu';
import { currentDevice, deviceEntries } from './cardSize';
import { useBoardEnv } from './ui';

export function DisplayButton({ id, always }: { id: string; always?: boolean }) {
  const { ctl, reveal } = useBoardEnv();
  const current = currentDevice(ctl, id);
  return (
    <Menu
      className={`nodrag tile-display ${always || current ? 'is-shown' : ''}`}
      triggerClassName={current ? 'is-on' : undefined}
      icon={current?.group === 'phone' ? 'phone' : 'monitor'}
      label={current ? `Display size: ${current.label}, ${current.w} × ${current.h}` : 'Display size: Fit tile'}
      items={() => deviceEntries(ctl, [id], reveal)}
    />
  );
}
