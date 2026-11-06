// A card header's way into focus mode (focus.ts) and back. Shown like the display button: on hover
// or selection with a mouse, always on a touch screen, and always on the focused card.

import { Button } from '../components/Button';
import { focusCard, leaveFocus, useFocusedCard } from './focus';

export function FocusButton({ id }: { id: string }) {
  const on = useFocusedCard() === id;
  const label = on ? 'Back to the board' : 'Focus: fill the screen with this card';
  return (
    <Button
      size="sm"
      variant="ghost"
      iconOnly
      icon={on ? 'fullscreenExit' : 'fullscreen'}
      className={`nodrag tile-focus ${on ? 'is-shown' : ''}`}
      aria-label={label}
      aria-pressed={on}
      title={label}
      onClick={() => (on ? leaveFocus() : focusCard(id))}
    />
  );
}
