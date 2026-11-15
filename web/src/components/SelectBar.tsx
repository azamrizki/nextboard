// Select mode for a list (DESIGN.md §3.18): a "Select" button, a long press on a touch screen,
// Shift-click for a run, Ctrl/⌘-click to toggle one, and a select-all of what is shown. While it
// is on, a bar at the bottom of the screen holds the count and what can be done to them.

import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { clickSelect, NO_SELECTION, pickedInOrder, pruneSelection, toggleAllVisible, visibleState, type Selectable, type Selection } from '../lib/selection';
import { Button } from './Button';

export interface SelectMode {
  on: boolean;
  sel: Selection;
  /** The picked ones, in the order they are shown. */
  picked: string[];
  count: number;
  /** none / some / all of what is shown. */
  shown: 'none' | 'some' | 'all';
  start: (first?: string) => void;
  stop: () => void;
  has: (id: string) => boolean;
  /** A click on an item: toggles, or with Shift the run from the last one. */
  click: (id: string, e?: { shiftKey?: boolean; ctrlKey?: boolean; metaKey?: boolean }) => void;
  toggleAll: () => void;
  /** Props for an item: a long press with a finger starts select mode with it. */
  pressProps: (id: string) => {
    'data-longpress': '';
    onPointerDown: (e: ReactPointerEvent) => void;
    onContextMenu: (e: ReactMouseEvent) => void;
  };
}

/**
 * `order`: what is on screen, top to bottom (Shift runs and select-all go by it). `present`:
 * everything the view holds, shown or folded away; what leaves it leaves the selection.
 */
export function useSelectMode(order: string[], present: string[] = order, selectable?: Selectable): SelectMode {
  const [on, setOn] = useState(false);
  const [sel, setSel] = useState<Selection>(NO_SELECTION);
  const finger = useRef(false);
  const presentKey = present.join('\n');
  const selectableRef = useRef(selectable);
  selectableRef.current = selectable;

  const presentRef = useRef(present);
  presentRef.current = present;
  useEffect(() => {
    setSel((s) => pruneSelection(s, presentRef.current, selectableRef.current));
  }, [presentKey]);

  const stop = useCallback(() => {
    setOn(false);
    setSel(NO_SELECTION);
  }, []);

  const start = useCallback((first?: string) => {
    setOn(true);
    if (first && (selectableRef.current?.(first) ?? true)) setSel({ ids: new Set([first]), anchor: first });
  }, []);

  // Esc leaves select mode, unless a dialog or a menu is open and takes it
  useEffect(() => {
    if (!on) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || document.querySelector('.modal-layer, .menu-list')) return;
      const t = e.target as HTMLElement | null;
      if (t?.closest('input, textarea, select')) return;
      stop();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [on, stop]);

  const orderRef = useRef(order);
  orderRef.current = order;

  const picked = pickedInOrder(sel, present);
  return {
    on,
    sel,
    picked,
    count: picked.length,
    shown: visibleState(sel, order, selectable),
    start,
    stop,
    has: (id: string) => sel.ids.has(id),
    click: (id, e) => {
      setOn(true);
      setSel((s) => clickSelect(s, id, orderRef.current, { shift: e?.shiftKey, toggle: e?.ctrlKey || e?.metaKey }, selectableRef.current));
    },
    toggleAll: () => setSel((s) => toggleAllVisible(s, orderRef.current, selectableRef.current)),
    pressProps: (id: string) => ({
      'data-longpress': '' as const,
      onPointerDown: (e: ReactPointerEvent) => {
        finger.current = e.pointerType === 'touch';
      },
      onContextMenu: (e: ReactMouseEvent) => {
        // a right-click with a mouse keeps the browser's own menu
        if (!finger.current || (e.target as HTMLElement).closest('input, textarea, .menu')) return;
        e.preventDefault();
        if (!on) start(id);
        else setSel((s) => clickSelect(s, id, orderRef.current, {}, selectableRef.current));
      },
    }),
  };
}

/** Whether a click should pick instead of open: in select mode, or with Shift, Ctrl or ⌘ held. */
export function picks(mode: SelectMode, e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) {
  return mode.on || e.shiftKey || e.ctrlKey || e.metaKey;
}

/** "Select" when off; "Select all" / "Select none" and "Done" when on. */
export function SelectToggle({ mode, label, disabled }: { mode: SelectMode; label: string; disabled?: boolean }) {
  if (!mode.on) {
    return (
      <Button size="sm" icon="selectAll" disabled={disabled} onClick={() => mode.start()} title={`Select ${label} to act on several at once`}>
        Select
      </Button>
    );
  }
  return (
    <Button size="sm" icon={mode.shown === 'all' ? 'minus' : 'selectAll'} aria-pressed={mode.shown === 'all'} onClick={mode.toggleAll}>
      {mode.shown === 'all' ? 'Select none' : 'Select all'}
    </Button>
  );
}

/** The bar at the bottom of the screen while select mode is on. */
export function SelectBar({ mode, label, children }: { mode: SelectMode; label: string; children: ReactNode }) {
  if (!mode.on) return null;
  return createPortal(
    <div className="select-bar" role="toolbar" aria-label={`Selected ${label}`}>
      <span className="select-bar-count tabular" aria-live="polite">
        {mode.count ? `${mode.count} selected` : `Select ${label}`}
      </span>
      <div className="select-bar-actions">
        {children}
        <Button size="sm" variant="ghost" onClick={mode.stop}>
          Cancel
        </Button>
      </div>
    </div>,
    document.body,
  );
}

/** The box on an item in select mode; the item itself takes the click. */
export function SelectBox({ checked, disabled }: { checked: boolean; disabled?: boolean }) {
  return <span className={`select-box ${checked ? 'is-on' : ''} ${disabled ? 'is-off' : ''}`} aria-hidden="true" />;
}
