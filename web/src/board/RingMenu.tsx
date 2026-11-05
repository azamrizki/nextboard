// Right-click on the empty board (DESIGN.md §3.5): seven actions around More, and More is a second,
// wider ring around Back. An action runs at once or opens its panel in the ring's place, with a way
// back. One caption under the ring names the button under the pointer. Cards and connections keep
// their ordinary menus.

import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode, type SyntheticEvent } from 'react';
import { createPortal } from 'react-dom';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { useDismiss } from '../components/Menu';
import '../styles/ring.css';

export interface RingItem {
  icon: IconName;
  label: string;
  shortcut?: string;
  disabled?: boolean;
  /** Why it is disabled, for the caption. */
  note?: string;
  /** Runs at once; the ring closes first. */
  onSelect?: () => void;
  /** Or: a panel that takes the ring's place. */
  panel?: (close: () => void) => ReactNode;
}

interface RingMenuProps {
  /** Screen position (clientX/Y) of the pointer. */
  x: number;
  y: number;
  label: string;
  /** Clockwise from the top. */
  items: RingItem[];
  more: RingItem[];
  onClose: () => void;
}

const EDGE_GAP = 8;

const stop = (e: SyntheticEvent) => e.stopPropagation();
const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(v, Math.max(lo, hi)));

export function RingMenu({ x, y, label, items, more, onClose }: RingMenuProps) {
  /** null = the ring; 'more' = the second ring; a number = that item's panel. */
  const [view, setView] = useState<number | 'more' | null>(null);
  const [box, setBox] = useState<{ left: number; top: number } | null>(null);
  const [named, setNamed] = useState<RingItem | null>(null);
  // the caption keeps its text while it fades out
  const lastNamed = useRef<RingItem | null>(null);
  if (named) lastNamed.current = named;
  const rootRef = useRef<HTMLDivElement>(null);
  const backTo = useRef(0);
  const prevFocus = useRef(document.activeElement as HTMLElement | null);
  const panelItem = typeof view === 'number' ? items[view] : null;

  useDismiss(true, rootRef, onClose);

  // centred on the pointer, and inside the window also when a panel grows
  useLayoutEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    const place = () => {
      const w = el.offsetWidth;
      const h = el.offsetHeight;
      setBox({ left: clamp(x - w / 2, EDGE_GAP, window.innerWidth - w - EDGE_GAP), top: clamp(y - h / 2, EDGE_GAP, window.innerHeight - h - EDGE_GAP) });
    };
    place();
    const ro = new ResizeObserver(place);
    ro.observe(el);
    return () => ro.disconnect();
  }, [x, y, view]);

  // focus once placed (a hidden element can't take it): the first of More, where the ring was left,
  // or the panel's first field
  const placed = box !== null;
  useEffect(() => {
    if (!placed) return;
    const root = rootRef.current;
    if (view === null || view === 'more') root?.querySelectorAll<HTMLButtonElement>('.ring-btn')[view === 'more' ? 0 : backTo.current]?.focus();
    // a form that still loads takes the focus itself; until then Esc must reach the panel
    else (root?.querySelector<HTMLElement>('[data-autofocus]') ?? root?.querySelector<HTMLElement>('.ring-panel-body button:not(:disabled), .ring-panel-body input') ?? root)?.focus();
  }, [placed, view]);

  const close = (refocus: boolean) => {
    onClose();
    if (refocus) prevFocus.current?.focus?.();
  };

  const go = (next: number | 'more' | null) => {
    setNamed(null);
    lastNamed.current = null;
    setBox(null);
    setView(next);
  };

  const onRingKey = (e: KeyboardEvent<HTMLDivElement>) => {
    e.stopPropagation();
    const list = Array.from(rootRef.current?.querySelectorAll<HTMLButtonElement>('.ring-btn') ?? []);
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    const move = (n: number) => {
      e.preventDefault();
      list[(n + list.length) % list.length]?.focus();
    };
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') move(i + 1);
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') move(i - 1);
    else if (e.key === 'Home') move(0);
    else if (e.key === 'End') move(list.length - 1);
    else if (e.key === 'Escape' && view === 'more') {
      e.preventDefault();
      go(null);
    } else if (e.key === 'Escape' || e.key === 'Tab') {
      e.preventDefault();
      close(true);
    }
  };

  const onPanelKey = (e: KeyboardEvent<HTMLDivElement>) => {
    e.stopPropagation();
    if (e.key === 'Escape') {
      e.preventDefault();
      go(null);
    } else if (e.key === 'Tab') {
      // focus stays in the panel, like in a dialog
      const list = Array.from(rootRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input, select') ?? []);
      const i = list.indexOf(document.activeElement as HTMLElement);
      if (e.shiftKey ? i <= 0 : i === list.length - 1) {
        e.preventDefault();
        list[e.shiftKey ? list.length - 1 : 0]?.focus();
      }
    }
  };

  const pick = (it: RingItem, i: number) => {
    if (it.disabled) return;
    if (it.panel && view === null) {
      backTo.current = i;
      go(i);
    } else {
      close(false);
      it.onSelect?.();
    }
  };

  const style: CSSProperties = box ? { left: box.left, top: box.top } : { left: 0, top: 0, visibility: 'hidden' };
  const shown = lastNamed.current;
  const list = view === 'more' ? more : items;

  return createPortal(
    panelItem ? (
      <div
        ref={rootRef}
        className="ring-panel"
        tabIndex={-1}
        role="dialog"
        aria-label={panelItem.label}
        style={style}
        onKeyDown={onPanelKey}
        onClick={stop}
        onMouseDown={stop}
        onPointerDown={stop}
        onDoubleClick={stop}
        onContextMenu={(e) => {
          e.preventDefault();
          e.stopPropagation();
        }}
      >
        <header className="ring-panel-head">
          <Button size="sm" variant="ghost" iconOnly icon="chevronLeft" aria-label="Back" title="Back · Esc" onClick={() => go(null)} />
          <h2>{panelItem.label}</h2>
        </header>
        <div className="ring-panel-body">{panelItem.panel?.(() => close(false))}</div>
      </div>
    ) : (
      <div
        key={view === 'more' ? 'more' : 'ring'}
        ref={rootRef}
        className={`ring ${view === 'more' ? 'is-more' : ''}`}
        role="menu"
        aria-label={view === 'more' ? 'More actions' : label}
        style={style}
        onKeyDown={onRingKey}
        onClick={stop}
        onMouseDown={stop}
        onPointerDown={stop}
        onDoubleClick={stop}
        onContextMenu={(e) => {
          e.preventDefault();
          e.stopPropagation();
        }}
      >
        {list.map((it, i) => (
          <button
            key={it.label}
            type="button"
            role="menuitem"
            className={`ring-btn ${it.disabled ? 'is-disabled' : ''}`}
            style={{ '--ring-a': `${(360 / list.length) * i}deg` } as CSSProperties}
            aria-label={it.shortcut ? `${it.label} (${it.shortcut})` : it.label}
            aria-haspopup={it.panel ? 'dialog' : undefined}
            aria-disabled={it.disabled || undefined}
            tabIndex={-1}
            onClick={() => pick(it, i)}
            onPointerEnter={() => setNamed(it)}
            onPointerLeave={() => setNamed(null)}
            onFocus={(e) => {
              if (e.currentTarget.matches(':focus-visible')) setNamed(it);
            }}
            onBlur={() => setNamed(null)}
          >
            <Icon name={it.icon} />
          </button>
        ))}
        <button
          type="button"
          role="menuitem"
          className="ring-btn ring-hub"
          aria-haspopup={view === 'more' ? undefined : 'menu'}
          tabIndex={-1}
          onClick={() => {
            if (view === 'more') {
              backTo.current = items.length;
              go(null);
            } else go('more');
          }}
        >
          <Icon name={view === 'more' ? 'chevronLeft' : 'more'} />
          <span>{view === 'more' ? 'Back' : 'More'}</span>
        </button>
        <span className={`ring-caption ${named ? 'is-on' : ''}`} aria-hidden="true">
          {shown?.label}
          {shown?.disabled && shown.note ? <span className="ring-caption-note">{shown.note}</span> : shown?.shortcut ? <kbd>{shown.shortcut}</kbd> : null}
        </span>
      </div>
    ),
    document.body,
  );
}

export interface RingGridItem {
  key: string;
  mark: ReactNode;
  label: string;
  /** The whole name, when the label is a short one. */
  title?: string;
  /** The current choice (the agent the Spawn tool holds). */
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

/** The tiles of a ring panel: agents to spawn, shapes to place. */
export function RingGrid({ items, label, empty }: { items: RingGridItem[]; label: string; empty?: string }) {
  if (!items.length) return <p className="ring-empty">{empty}</p>;
  return (
    <div className="ring-grid nowheel" role="group" aria-label={label}>
      {items.map((it) => (
        <button key={it.key} type="button" className={`ring-tile ${it.active ? 'is-active' : ''}`} title={it.title ?? it.label} aria-label={it.title ?? it.label} aria-current={it.active || undefined} disabled={it.disabled} onClick={it.onClick}>
          <span className="ring-tile-mark">{it.mark}</span>
          <span className="ring-tile-name truncate">{it.label}</span>
        </button>
      ))}
    </div>
  );
}
