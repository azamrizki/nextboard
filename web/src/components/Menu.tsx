// Dropdown menu (DESIGN.md §3.6): glass, arrow-key navigation, Esc closes and returns
// focus to the trigger. Rendered in a portal so it isn't scaled/clipped by React Flow nodes.
// <ContextMenu> shows the same list at a pointer position (right-click menus).

import {
  useLayoutEffect,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type RefObject,
  type SyntheticEvent,
} from 'react';
import { createPortal } from 'react-dom';
import { Button } from './Button';
import { Icon, type IconName } from './Icon';

export interface MenuItem {
  label: string;
  onSelect: () => void;
  icon?: IconName;
  disabled?: boolean;
  danger?: boolean;
  title?: string;
  /** Keyboard shortcut hint shown on the right ("Ctrl+D"). */
  shortcut?: string;
  /** Shows a check mark (current choice). */
  checked?: boolean;
  /** A value on the right of the row ("1920 × 1080"). */
  meta?: string;
  /** Selecting it leaves the menu open: the menu's owner decides what comes next. */
  keepOpen?: boolean;
  /** Levels in from the left, for a tree. Needs a list class that sets the padding. */
  indent?: number;
  /** A tree row that opens and closes the rows below it: → and ← do that too. */
  expanded?: boolean;
}

/** A labelled row of colour swatches (menuitemradio) or icon buttons (menuitem). */
export interface MenuSwatches {
  type: 'swatches';
  label: string;
  value: string | undefined;
  options: { value: string; label: string; color?: string; icon?: IconName }[];
  onSelect: (value: string) => void;
}

/** A labelled row of a few text options, one of them chosen (menuitemradio). The menu stays open,
 * so what the choice changes shows at once. */
export interface MenuChoice {
  type: 'choice';
  label: string;
  value: string;
  options: { value: string; label: string; title?: string }[];
  onSelect: (value: string) => void;
}

/** Non-interactive section heading. */
export interface MenuHeading {
  type: 'heading';
  label: string;
}

/** − value + row that stays open while stepping (e.g. text size). */
export interface MenuStepper {
  type: 'stepper';
  label: string;
  value: string;
  /** undefined = at the limit (disabled). */
  onDec?: () => void;
  onInc?: () => void;
  onReset?: () => void;
  decLabel: string;
  incLabel: string;
  resetLabel?: string;
}

export type MenuEntry = MenuItem | MenuSwatches | MenuChoice | MenuHeading | MenuStepper | 'sep';

interface MenuProps {
  label: string;
  icon?: IconName;
  /** A function is only called while the menu is open. */
  items: MenuEntry[] | (() => MenuEntry[]);
  /** Extra class names for the trigger wrapper (e.g. "nodrag" inside React Flow nodes). */
  className?: string;
  /** Trigger variant / text (defaults to an icon-only ghost button). */
  triggerLabel?: string;
  /** Extra class names for the trigger button itself. */
  triggerClassName?: string;
}

const stop = (e: SyntheticEvent) => e.stopPropagation();

function isEntryItem(e: MenuEntry): e is MenuItem {
  return typeof e === 'object' && !('type' in e);
}

interface MenuListProps {
  label: string;
  items: MenuEntry[];
  style: CSSProperties;
  listRef: RefObject<HTMLDivElement | null>;
  onClose: (refocus: boolean) => void;
  className?: string;
}

function MenuList({ label, items, style, listRef, onClose, className }: MenuListProps) {
  const onListKey = (e: KeyboardEvent<HTMLDivElement>) => {
    e.stopPropagation();
    const list = Array.from(listRef.current?.querySelectorAll<HTMLButtonElement>('.menu-item:not(:disabled)') ?? []);
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    const inRow = (document.activeElement as HTMLElement | null)?.closest('.menu-swatches');
    const expanded = (document.activeElement as HTMLElement | null)?.getAttribute('aria-expanded');
    if (expanded && (e.key === 'ArrowRight' || e.key === 'ArrowLeft')) {
      e.preventDefault();
      if ((expanded === 'true') === (e.key === 'ArrowLeft')) (document.activeElement as HTMLElement).click();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (inRow) {
        // jump past the swatch row
        const after = list.findIndex((b, k) => k > i && !b.closest('.menu-swatches'));
        list[after >= 0 ? after : 0]?.focus();
      } else list[(i + 1) % list.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (inRow) {
        let k = i - 1;
        while (k >= 0 && list[k].closest('.menu-swatches') === inRow) k--;
        list[(k + list.length) % list.length]?.focus();
      } else list[(i - 1 + list.length) % list.length]?.focus();
    } else if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && inRow) {
      e.preventDefault();
      list[e.key === 'ArrowRight' ? Math.min(list.length - 1, i + 1) : Math.max(0, i - 1)]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      list[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      list[list.length - 1]?.focus();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      onClose(true);
    } else if (e.key === 'Tab') {
      e.preventDefault();
      onClose(true);
    }
  };

  return createPortal(
    <div
      ref={listRef}
      className={`menu-list ${className ?? ''}`}
      role="menu"
      aria-label={label}
      style={style}
      // Escape closes the menu and not the modal it was opened from.
      data-keep-escape=""
      onKeyDown={onListKey}
      onClick={stop}
      onMouseDown={stop}
      onPointerDown={stop}
      onDoubleClick={stop}
      onContextMenu={(e) => {
        e.preventDefault();
        e.stopPropagation();
      }}
    >
      {items.map((it, i) => {
        if (it === 'sep') return <div key={`sep-${i}`} className="menu-sep" role="separator" />;
        if (!isEntryItem(it)) {
          if (it.type === 'heading')
            return (
              <div key={`h-${i}`} className="menu-heading" role="presentation">
                {it.label}
              </div>
            );
          if (it.type === 'choice')
            return (
              // .menu-swatches: ← and → move along the row, ↓ and ↑ past it
              <div key={`ch-${i}`} className="menu-swatches menu-choice" role="group" aria-label={it.label}>
                <span className="menu-swatches-label">{it.label}</span>
                <span className="menu-swatches-row">
                  {it.options.map((o) => (
                    <button
                      key={o.value}
                      type="button"
                      role="menuitemradio"
                      aria-checked={it.value === o.value}
                      title={o.title}
                      tabIndex={-1}
                      className="menu-item choice-item"
                      onClick={() => it.onSelect(o.value)}
                    >
                      {o.label}
                    </button>
                  ))}
                </span>
              </div>
            );
          if (it.type === 'stepper')
            return (
              <div key={`st-${i}`} className="menu-swatches menu-stepper" role="group" aria-label={`${it.label}: ${it.value}`}>
                <span className="menu-swatches-label">{it.label}</span>
                <span className="menu-swatches-row">
                  <button type="button" role="menuitem" tabIndex={-1} className="menu-item icon-item" aria-label={it.decLabel} title={it.decLabel} disabled={!it.onDec} onClick={() => it.onDec?.()}>
                    <Icon name="minus" />
                  </button>
                  <span className="menu-stepper-value tabular" aria-live="polite">
                    {it.value}
                  </span>
                  <button type="button" role="menuitem" tabIndex={-1} className="menu-item icon-item" aria-label={it.incLabel} title={it.incLabel} disabled={!it.onInc} onClick={() => it.onInc?.()}>
                    <Icon name="plus" />
                  </button>
                  {it.resetLabel && (
                    <button type="button" role="menuitem" tabIndex={-1} className="menu-item menu-stepper-reset" disabled={!it.onReset} onClick={() => it.onReset?.()}>
                      {it.resetLabel}
                    </button>
                  )}
                </span>
              </div>
            );
          return (
            <div key={`sw-${i}`} className="menu-swatches" role="group" aria-label={it.label}>
              <span className="menu-swatches-label">{it.label}</span>
              <span className="menu-swatches-row">
                {it.options.map((o) => (
                  <button
                    key={o.value}
                    type="button"
                    role={o.icon ? 'menuitem' : 'menuitemradio'}
                    aria-checked={o.icon ? undefined : it.value === o.value}
                    aria-label={o.label}
                    title={o.label}
                    tabIndex={-1}
                    className={`menu-item ${o.icon ? 'icon-item' : 'swatch-item'}`}
                    style={o.color ? ({ '--swatch': o.color } as CSSProperties) : undefined}
                    onClick={() => {
                      onClose(true);
                      it.onSelect(o.value);
                    }}
                  >
                    {o.icon && <Icon name={o.icon} />}
                  </button>
                ))}
              </span>
            </div>
          );
        }
        return (
          <button
            key={`${it.label}-${i}`}
            type="button"
            role={it.checked !== undefined ? 'menuitemcheckbox' : 'menuitem'}
            aria-checked={it.checked}
            aria-expanded={it.expanded}
            tabIndex={-1}
            className={`menu-item ${it.danger ? 'danger' : ''}`}
            style={it.indent ? ({ '--menu-indent': it.indent } as CSSProperties) : undefined}
            disabled={it.disabled}
            title={it.title}
            onClick={() => {
              if (!it.keepOpen) onClose(true);
              it.onSelect();
            }}
          >
            {it.icon ? <Icon name={it.icon} /> : it.checked !== undefined ? <span className="menu-icon-gap" /> : null}
            <span className="menu-label">{it.label}</span>
            {it.meta && <span className="menu-meta tabular">{it.meta}</span>}
            {it.checked && <Icon name="check" className="menu-check" />}
            {it.shortcut && <kbd className="menu-kbd">{it.shortcut}</kbd>}
          </button>
        );
      })}
    </div>,
    document.body,
  );
}

/** Close on outside pointer, wheel (the canvas may move under a fixed menu), resize. */
export function useDismiss(open: boolean, listRef: RefObject<HTMLDivElement | null>, close: () => void, ignore?: HTMLElement | null) {
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    if (!open) return;
    // a menu opened from inside this one lives in its own portal, and is not "outside"
    const inMenu = (target: Node) => target instanceof Element && !!target.closest('.menu-list');
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (!listRef.current?.contains(target) && !ignore?.contains(target) && !inMenu(target)) closeRef.current();
    };
    const onWheel = (e: WheelEvent) => {
      if (!listRef.current?.contains(e.target as Node) && !inMenu(e.target as Node)) closeRef.current();
    };
    const onResize = () => closeRef.current();
    // Escape even when focus didn't make it into the list
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !listRef.current?.contains(e.target as Node)) closeRef.current();
    };
    document.addEventListener('pointerdown', onDown, true);
    document.addEventListener('wheel', onWheel, { capture: true, passive: true });
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', onDown, true);
      document.removeEventListener('wheel', onWheel, true);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', onResize);
    };
  }, [open, listRef, ignore]);
}

export function Menu({ label, icon = 'more', items, className, triggerLabel, triggerClassName }: MenuProps) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const close = (refocus = true) => {
    setOpen(false);
    setPos(null);
    if (refocus) triggerRef.current?.focus();
  };

  // Position under the trigger (right-aligned); flip above if it would overflow.
  useLayoutEffect(() => {
    if (!open) return;
    const t = triggerRef.current?.getBoundingClientRect();
    if (!t) return;
    const gap = 6;
    let top = t.bottom + gap;
    const h = listRef.current?.offsetHeight ?? 0;
    if (top + h > window.innerHeight - gap && t.top - gap - h > 0) top = t.top - gap - h;
    top = Math.max(gap, Math.min(top, window.innerHeight - h - gap));
    setPos({ top, right: Math.max(gap, window.innerWidth - t.right) });
  }, [open]);

  // focus once positioned (a visibility:hidden list can't take focus)
  const placed = open && pos !== null;
  useEffect(() => {
    if (placed) listRef.current?.querySelector<HTMLButtonElement>('.menu-item:not(:disabled)')?.focus();
  }, [placed]);

  useDismiss(open, listRef, () => close(false), triggerRef.current);

  return (
    <div className={`menu ${className ?? ''}`}>
      <Button
        ref={triggerRef}
        variant="ghost"
        size="sm"
        iconOnly={!triggerLabel}
        icon={icon}
        className={triggerClassName}
        aria-label={triggerLabel ? undefined : label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation();
          if (open) close(false);
          else setOpen(true);
        }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && !open) {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        {triggerLabel}
      </Button>
      {open && (
        <MenuList
          label={label}
          items={typeof items === 'function' ? items() : items}
          listRef={listRef}
          onClose={close}
          style={pos ? { top: pos.top, right: pos.right } : { visibility: 'hidden', top: 0, right: 0 }}
        />
      )}
    </div>
  );
}

interface ContextMenuProps {
  /** Screen position (clientX/Y) of the pointer. */
  x: number;
  y: number;
  label: string;
  items: MenuEntry[];
  onClose: () => void;
  /** Hangs from y and scrolls, for rows that open and close: it never rises over its button. */
  anchored?: boolean;
  className?: string;
}

/** Menu at the pointer (right-click). Focus moves into it; Esc / outside click closes. */
export function ContextMenu({ x, y, label, items, onClose, anchored, className }: ContextMenuProps) {
  const listRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number; maxHeight?: number } | null>(null);
  const prevFocus = useRef(document.activeElement as HTMLElement | null);

  useLayoutEffect(() => {
    const el = listRef.current;
    const w = el?.offsetWidth ?? 0;
    const h = el?.offsetHeight ?? 0;
    const gap = 8;
    const left = Math.max(gap, Math.min(x, window.innerWidth - w - gap));
    setPos(anchored ? { left, top: y, maxHeight: window.innerHeight - y - gap } : { left, top: Math.max(gap, Math.min(y, window.innerHeight - h - gap)) });
  }, [x, y, anchored]);

  const placed = pos !== null;
  useEffect(() => {
    if (!placed) return;
    const list = listRef.current;
    // a long list opens at the current choice
    ((anchored ? list?.querySelector<HTMLButtonElement>('.menu-item[aria-checked="true"]') : null) ?? list?.querySelector<HTMLButtonElement>('.menu-item:not(:disabled)'))?.focus();
  }, [placed, anchored]);

  const close = (refocus: boolean) => {
    onClose();
    if (refocus) prevFocus.current?.focus?.();
  };
  useDismiss(true, listRef, () => onClose());

  return (
    <MenuList
      label={label}
      items={items}
      listRef={listRef}
      onClose={close}
      className={className}
      style={pos ? { left: pos.left, top: pos.top, maxHeight: pos.maxHeight } : { visibility: 'hidden', left: 0, top: 0 }}
    />
  );
}
