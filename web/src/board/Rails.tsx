// Board chrome: left tool rail (single-key tools), right rail (what a project is worked with),
// zoom controls. On narrow screens the right rail's items and the inserts are a flyout on the left.
// Rails are glass toolbars (role="toolbar") with arrow-key navigation.

import { createPortal } from 'react-dom';
import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type MouseEvent as ReactMouseEvent, type ReactNode } from 'react';
import { useReactFlow, useStore } from '@xyflow/react';
import { Icon, type IconName } from '../components/Icon';
import type { ShortcutMap } from '../lib/types';
import { useFullscreen } from '../lib/useFullscreen';
import { useMediaQuery } from '../lib/useMediaQuery';
import type { Tool } from './ui';

/** Arrow keys move focus between the buttons of a toolbar. */
function onToolbarKey(e: KeyboardEvent<HTMLElement>, vertical: boolean) {
  const keys = vertical ? ['ArrowDown', 'ArrowUp'] : ['ArrowRight', 'ArrowLeft'];
  if (![...keys, 'Home', 'End'].includes(e.key)) return;
  const items = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>(':scope > .rail-btn:not(:disabled), :scope > .rail-group > .rail-btn:not(:disabled)'));
  const i = items.indexOf(document.activeElement as HTMLButtonElement);
  if (i < 0) return;
  e.preventDefault();
  const next = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : e.key === keys[0] ? (i + 1) % items.length : (i - 1 + items.length) % items.length;
  items[next]?.focus();
}

interface RailButtonProps {
  icon: IconName;
  label: string;
  shortcut?: string;
  active?: boolean;
  disabled?: boolean;
  onClick: (e: ReactMouseEvent<HTMLButtonElement>) => void;
  expanded?: boolean;
  badge?: ReactNode;
}

/** A short wait, so that a pointer passing over the rail does not flash every name. */
const TIP_DELAY_MS = 80;
const TIP_GAP = 8;

interface TipPlace {
  x: number;
  y: number;
  side: 'left' | 'right' | 'top';
}

/** Beside the button, on the side facing the board; above it in a row of buttons. */
function tipPlace(el: HTMLElement): TipPlace {
  const r = el.getBoundingClientRect();
  if (el.closest('.rail-flyout, .rail-flyout-row')) return { x: r.left + r.width / 2, y: r.top - TIP_GAP, side: 'top' };
  if (el.closest('.rail-right')) return { x: r.left - TIP_GAP, y: r.top + r.height / 2, side: 'left' };
  return { x: r.right + TIP_GAP, y: r.top + r.height / 2, side: 'right' };
}

export function RailButton({ icon, label, shortcut, active, disabled, onClick, expanded, badge }: RailButtonProps) {
  // The browser's own title takes a second or two to appear, and a rail is all icons.
  const [tip, setTip] = useState<TipPlace | null>(null);
  const timer = useRef(0);
  const hide = () => {
    window.clearTimeout(timer.current);
    setTip(null);
  };
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return (
    <button
      type="button"
      className={`rail-btn ${active ? 'active' : ''}`}
      aria-label={shortcut ? `${label} (${shortcut})` : label}
      aria-pressed={active}
      aria-expanded={expanded}
      disabled={disabled}
      onClick={onClick}
      onPointerEnter={(e) => {
        if (e.pointerType !== 'mouse') return;
        const el = e.currentTarget;
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setTip(tipPlace(el)), TIP_DELAY_MS);
      }}
      onPointerLeave={hide}
      onPointerDown={hide}
      onFocus={(e) => {
        if (e.currentTarget.matches(':focus-visible')) setTip(tipPlace(e.currentTarget));
      }}
      onBlur={hide}
    >
      <Icon name={icon} />
      {badge}
      {tip &&
        createPortal(
          <span className={`rail-tip is-${tip.side}`} style={{ left: tip.x, top: tip.y }} aria-hidden="true">
            {label}
            {shortcut && <kbd>{shortcut}</kbd>}
          </span>,
          document.body,
        )}
    </button>
  );
}

const SHAPE_TOOLS: { tool: Tool; icon: IconName; label: string }[] = [
  { tool: 'rect', icon: 'square', label: 'Rectangle' },
  { tool: 'ellipse', icon: 'circle', label: 'Ellipse' },
  { tool: 'diamond', icon: 'diamond', label: 'Diamond' },
  { tool: 'line', icon: 'line', label: 'Line' },
  { tool: 'arrow', icon: 'arrowUpRight', label: 'Arrow' },
  { tool: 'note', icon: 'note', label: 'Sticky note' },
];
const SHAPE_SET = new Set(SHAPE_TOOLS.map((s) => s.tool));

interface ToolRailProps {
  tool: Tool;
  onTool: (t: Tool) => void;
  onFit: () => void;
  onUndo: () => void;
  onRedo: () => void;
  canUndo: boolean;
  canRedo: boolean;
  /** Narrow screens: the insert rail's items live in a flyout here. */
  insert?: ReactNode;
  terminalHint: string;
  shortcuts: ShortcutMap;
  /** Spawn tool: the agent flyout (rendered next to the rail while open). */
  spawnOpen: boolean;
  spawnFlyout: ReactNode;
  onSpawnClick: () => void;
  onSpawnClose: () => void;
  /** A phone: the drawing tools, Redo and Full screen move to the Insert grid, so the rail fits. */
  short?: boolean;
}

export function ToolRail({
  tool,
  onTool,
  onFit,
  onUndo,
  onRedo,
  canUndo,
  canRedo,
  insert,
  terminalHint,
  shortcuts,
  spawnOpen,
  spawnFlyout,
  onSpawnClick,
  onSpawnClose,
  short = false,
}: ToolRailProps) {
  const [shapesOpen, setShapesOpen] = useState(false);
  const [insertOpen, setInsertOpen] = useState(false);
  const [lastShape, setLastShape] = useState<Tool>('rect');
  const [flyTop, setFlyTop] = useState(0);
  const fullscreen = useFullscreen();
  const ref = useRef<HTMLDivElement>(null);
  const shapeIcon = SHAPE_TOOLS.find((s) => s.tool === (SHAPE_SET.has(tool) ? tool : lastShape))?.icon ?? 'shapes';

  const spawnCloseRef = useRef(onSpawnClose);
  spawnCloseRef.current = onSpawnClose;
  useEffect(() => {
    if (!shapesOpen && !insertOpen && !spawnOpen) return;
    const close = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node) && !(e.target as HTMLElement | null)?.closest?.('.modal-layer')) {
        setShapesOpen(false);
        setInsertOpen(false);
        spawnCloseRef.current();
      }
    };
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer')) {
        setShapesOpen(false);
        setInsertOpen(false);
        spawnCloseRef.current();
      }
    };
    document.addEventListener('pointerdown', close, true);
    document.addEventListener('keydown', esc, true);
    return () => {
      document.removeEventListener('pointerdown', close, true);
      document.removeEventListener('keydown', esc, true);
    };
  }, [shapesOpen, insertOpen, spawnOpen]);

  const pick = (t: Tool) => {
    onTool(tool === t && t !== 'select' ? 'select' : t);
    setShapesOpen(false);
    setInsertOpen(false);
    onSpawnClose();
  };
  const key = (k: string) => k.toUpperCase();
  // the flyout can also open from the keyboard: align it with the Spawn button
  const [spawnTop, setSpawnTop] = useState(0);
  useLayoutEffect(() => {
    if (!spawnOpen) return;
    const btn = ref.current?.querySelectorAll<HTMLElement>('.rail-group > .rail-btn')[2];
    if (btn) setSpawnTop(btn.offsetTop);
  }, [spawnOpen]);

  return (
    <div className="rail rail-left" ref={ref}>
      <div className="rail-group" role="toolbar" aria-label="Tools" aria-orientation="vertical" onKeyDown={(e) => onToolbarKey(e, true)}>
        <RailButton icon="cursor" label="Select" shortcut={key(shortcuts.select)} active={tool === 'select'} onClick={() => pick('select')} />
        <RailButton icon="hand" label="Hand — move the board" shortcut={key(shortcuts.hand)} active={tool === 'hand'} onClick={() => pick('hand')} />
        <RailButton
          icon="terminal"
          label={`Spawn agent${terminalHint ? ` — ${terminalHint}` : ''}`}
          shortcut={key(shortcuts.spawn)}
          active={tool === 'spawn'}
          expanded={spawnOpen}
          onClick={() => {
            setShapesOpen(false);
            setInsertOpen(false);
            onSpawnClick();
          }}
        />
        {!short && (
        <RailButton
          icon={shapeIcon}
          label="Shapes and notes"
          active={SHAPE_SET.has(tool)}
          expanded={shapesOpen}
          onClick={(e) => {
            setFlyTop(e.currentTarget.offsetTop);
            setInsertOpen(false);
            setShapesOpen((o) => !o);
          }}
        />
        )}
        {!short && (
          <>
            <RailButton icon="type" label="Text" active={tool === 'text'} onClick={() => pick('text')} />
            <RailButton icon="pen" label="Pen" shortcut={key(shortcuts.pen)} active={tool === 'pen'} onClick={() => pick('pen')} />
            <RailButton icon="eraser" label="Eraser" shortcut={key(shortcuts.eraser)} active={tool === 'eraser'} onClick={() => pick('eraser')} />
            <RailButton icon="image" label="Image" active={tool === 'image'} onClick={() => pick('image')} />
          </>
        )}
        {insert && (
          <RailButton
            icon="plus"
            label="Insert"
            expanded={insertOpen}
            onClick={(e) => {
              setFlyTop(e.currentTarget.offsetTop);
              setShapesOpen(false);
              setInsertOpen((o) => !o);
            }}
          />
        )}
        <span className="rail-sep" aria-hidden="true" />
        <RailButton icon="undo" label="Undo" shortcut="Ctrl+Z" disabled={!canUndo} onClick={onUndo} />
        {!short && <RailButton icon="redo" label="Redo" shortcut="Ctrl+Shift+Z" disabled={!canRedo} onClick={onRedo} />}
        <RailButton icon="fit" label="Fit to content" shortcut="Shift+1" onClick={onFit} />
        {fullscreen.supported && !short && (
          <RailButton icon={fullscreen.active ? 'fullscreenExit' : 'fullscreen'} label={fullscreen.active ? 'Exit full screen' : 'Full screen'} onClick={fullscreen.toggle} />
        )}
      </div>
      {shapesOpen && (
        <div className="rail-flyout" role="toolbar" aria-label="Shapes" style={{ '--fly-top': `${flyTop}px` } as CSSProperties} onKeyDown={(e) => onToolbarKey(e, false)}>
          {SHAPE_TOOLS.map((s) => (
            <RailButton
              key={s.tool}
              icon={s.icon}
              label={s.label}
              active={tool === s.tool}
              onClick={() => {
                setLastShape(s.tool);
                pick(s.tool);
              }}
            />
          ))}
        </div>
      )}
      {spawnOpen && (
        <div className="rail-flyout rail-flyout-agents" style={{ '--fly-top': `${spawnTop}px` } as CSSProperties}>
          {spawnFlyout}
        </div>
      )}
      {insertOpen && insert && (
        <div className="rail-flyout rail-flyout-insert" style={{ '--fly-top': `${flyTop}px` } as CSSProperties} onClick={() => setInsertOpen(false)}>
          {insert}
        </div>
      )}
    </div>
  );
}

export interface InsertItem {
  icon: IconName;
  label: string;
  shortcut?: string;
  onClick: (e: ReactMouseEvent<HTMLButtonElement>) => void;
  active?: boolean;
  disabled?: boolean;
  badge?: ReactNode;
  /** Separator before this item (vertical rail). */
  sepBefore?: boolean;
}

export function InsertRail({ items }: { items: InsertItem[] }) {
  const compact = useMediaQuery('(max-width: 899px)');
  if (compact) return null;
  return (
    <div className="rail rail-right">
      <div className="rail-group" role="toolbar" aria-label="Insert" aria-orientation="vertical" onKeyDown={(e) => onToolbarKey(e, true)}>
        {items.map((it) => [
          it.sepBefore ? <span key={`sep-${it.label}`} className="rail-sep" aria-hidden="true" /> : null,
          <RailButton key={it.label} icon={it.icon} label={it.label} shortcut={it.shortcut} active={it.active} disabled={it.disabled} onClick={it.onClick} badge={it.badge} />,
        ])}
      </div>
    </div>
  );
}

/**
 * Insert items in the left rail's flyout (narrow screens, touch screens). A finger has no tooltip
 * to read, so on a touch screen each one carries its name.
 */
export function InsertButtons({ items, labelled }: { items: InsertItem[]; labelled?: boolean }) {
  if (labelled)
    return (
      <div className="rail-flyout-row insert-tiles" role="toolbar" aria-label="Insert" onKeyDown={(e) => onToolbarKey(e, false)}>
        {items.map((it) => (
          <button key={it.label} type="button" className={`insert-tile ${it.active ? 'active' : ''}`} aria-pressed={it.active} disabled={it.disabled} onClick={it.onClick}>
            <Icon name={it.icon} />
            <span className="insert-tile-label">{it.label}</span>
          </button>
        ))}
      </div>
    );
  return (
    <div className="rail-flyout-row" role="toolbar" aria-label="Insert" onKeyDown={(e) => onToolbarKey(e, false)}>
      {items.map((it) => (
        <RailButton key={it.label} icon={it.icon} label={it.label} shortcut={it.shortcut} active={it.active} disabled={it.disabled} onClick={it.onClick} />
      ))}
    </div>
  );
}

/** Zoom out / level (click = 100 %) / zoom in, bottom-left. Range 20–300 %. */
export function ZoomControls() {
  const rf = useReactFlow();
  const zoom = useStore((s) => s.transform[2]);
  return (
    <div className="zoom-controls" role="toolbar" aria-label="Zoom" onKeyDown={(e) => onToolbarKey(e, false)}>
      <button type="button" className="rail-btn" aria-label="Zoom out (Ctrl+−)" title="Zoom out · Ctrl+−" disabled={zoom <= 0.2} onClick={() => void rf.zoomOut({ duration: 150 })}>
        <Icon name="minus" />
      </button>
      <button type="button" className="rail-btn zoom-level tabular" aria-label={`Zoom ${Math.round(zoom * 100)}%, reset to 100%`} title="Reset to 100% · Ctrl+0" onClick={() => void rf.zoomTo(1, { duration: 150 })}>
        {Math.round(zoom * 100)}%
      </button>
      <button type="button" className="rail-btn" aria-label="Zoom in (Ctrl+=)" title="Zoom in · Ctrl+=" disabled={zoom >= 3} onClick={() => void rf.zoomIn({ duration: 150 })}>
        <Icon name="plus" />
      </button>
    </div>
  );
}
