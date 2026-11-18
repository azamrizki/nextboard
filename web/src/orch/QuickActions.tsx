// Quick actions (DESIGN.md §3.12): one round button whose actions open upward from it as pills,
// the first right above the button, like a phone's speed dial. The command bar has it left of
// its input; the code view's Lead pane has it in its composer (studio/code/AgentPanes.tsx).
// Arrow keys move between the actions, Esc closes and gives the keyboard back to the button, a
// press or focus anywhere else closes.

import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { Icon } from '../components/Icon';
import '../styles/orch.css';

export interface QuickAction {
  id: string;
  label: string;
  onClick: () => void;
  title?: string;
  danger?: boolean;
  /** Says something is on (Resume all while agents are paused). */
  pressed?: boolean;
  /** The second, arming click of a two-click action (Kill all). */
  armed?: boolean;
  badge?: number;
  /** The actions stay open after it (the kill switch's first, arming click). */
  keepOpen?: boolean;
}

export function QuickActions({ actions, dot = false, title = 'Quick actions: new consoles, groups, layouts, arrange, pause, memory', className = '' }: { actions: QuickAction[]; dot?: boolean; title?: string; className?: string }) {
  const [dial, setDial] = useState(false);
  const fabRef = useRef<HTMLButtonElement>(null);
  const dialRef = useRef<HTMLDivElement>(null);
  /** focus: back to the button (Esc, the button itself), not after an action that opens something. */
  const closeDial = (focus: boolean) => {
    setDial(false);
    if (focus) fabRef.current?.focus();
  };
  // open: the first action takes the keyboard; a press anywhere else, or the focus leaving, closes
  useEffect(() => {
    if (!dial) return;
    dialRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus();
    const outside = (e: Event) => {
      const t = e.target as Node | null;
      if (t && !dialRef.current?.contains(t) && !fabRef.current?.contains(t)) setDial(false);
    };
    document.addEventListener('pointerdown', outside, true);
    document.addEventListener('focusin', outside, true);
    return () => {
      document.removeEventListener('pointerdown', outside, true);
      document.removeEventListener('focusin', outside, true);
    };
  }, [dial]);

  return (
    <div className={`cmd-fab-wrap ${dial ? 'is-open' : ''} ${className}`}>
      {dial && (
        <div
          className="cmd-dial"
          role="menu"
          aria-label="Quick actions"
          ref={dialRef}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault();
              e.stopPropagation();
              closeDial(true);
              return;
            }
            if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown' && e.key !== 'Home' && e.key !== 'End') return;
            // the first action sits next to the button, the list grows upward
            const items = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'));
            const i = items.indexOf(document.activeElement as HTMLButtonElement);
            e.preventDefault();
            const n = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (Math.max(i, 0) + (e.key === 'ArrowUp' ? 1 : items.length - 1)) % items.length;
            items[n]?.focus();
          }}
        >
          {actions.map((c, i) => (
            <button
              key={c.id}
              type="button"
              role="menuitem"
              className={`cmd-chip ${c.danger ? 'is-danger' : ''} ${c.armed ? 'is-armed' : ''} ${c.pressed ? 'is-pressed' : ''}`}
              style={{ '--i': i } as CSSProperties}
              title={c.title ?? c.label}
              aria-description={c.pressed ? 'Some agents are paused' : undefined}
              onClick={() => {
                if (!c.keepOpen) closeDial(false);
                c.onClick();
              }}
            >
              <span>{c.label}</span>
              {!!c.badge && <span className="chip-count tabular">{c.badge}</span>}
            </button>
          ))}
        </div>
      )}
      <button
        type="button"
        ref={fabRef}
        className="cmd-fab"
        aria-label="Quick actions"
        aria-haspopup="menu"
        aria-expanded={dial}
        title={dial ? 'Close quick actions' : title}
        onClick={() => (dial ? closeDial(false) : setDial(true))}
      >
        <Icon name={dial ? 'close' : 'plus'} />
        {!dial && dot && <span className="cmd-fab-dot" aria-hidden="true" />}
      </button>
    </div>
  );
}
