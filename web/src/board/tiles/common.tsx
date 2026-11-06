// Shared tile pieces: the frame (selection ring + resize handles with undo), editable
// text drafts, inline title editing and auto-fitting text.

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactElement,
  type ReactNode,
  type RefObject,
} from 'react';
import { Handle, NodeResizer, Position } from '@xyflow/react';
import { Icon, type IconName } from '../../components/Icon';
import { Menu, type MenuEntry, type MenuItem } from '../../components/Menu';
import type { NodeLayout } from '../../lib/types';
import { rectOf } from '../controller';
import { DisplayButton } from '../DisplayButton';
import { FocusButton } from '../FocusButton';
import { CONTENT_SOURCES } from '../groups';
import { useBoardEnv } from '../ui';

/** Undo step for a resize gesture (the live changes arrive through onNodesChange). */
export function useResizeHistory(id: string) {
  const { ctl } = useBoardEnv();
  const start = useRef<NodeLayout | null>(null);
  const onResizeStart = useCallback(() => {
    const n = ctl.node(id);
    start.current = n ? rectOf(n) : null;
  }, [ctl, id]);
  const onResizeEnd = useCallback(() => {
    const before = start.current;
    start.current = null;
    const n = ctl.node(id);
    if (!before || !n) return;
    const after = rectOf(n);
    if (before.x === after.x && before.y === after.y && before.w === after.w && before.h === after.h) return;
    ctl.record([{ op: 'patch', id, set: before }], [{ op: 'patch', id, set: after }]);
  }, [ctl, id]);
  return { onResizeStart, onResizeEnd };
}

interface TileFrameProps {
  id: string;
  kind: string;
  selected: boolean;
  label: string;
  children: ReactNode;
  resizable?: boolean;
  minW?: number;
  minH?: number;
  keepAspect?: boolean;
  className?: string;
  style?: CSSProperties;
}

/** Outer frame of every non-terminal tile. No backdrop-filter (DESIGN.md §3.5). */
export function TileFrame({
  id,
  kind,
  selected,
  label,
  children,
  resizable = true,
  minW = 80,
  minH = 40,
  keepAspect,
  className,
  style,
}: TileFrameProps) {
  const resize = useResizeHistory(id);
  return (
    <>
      {resizable && (
        <NodeResizer
          isVisible={selected}
          minWidth={minW}
          minHeight={minH}
          keepAspectRatio={keepAspect}
          lineClassName="nb-resize-line"
          handleClassName="nb-resize-handle"
          onResizeStart={resize.onResizeStart}
          onResizeEnd={resize.onResizeEnd}
        />
      )}
      <div
        className={`tile tile-${kind} ${selected ? 'selected' : ''} ${className ?? ''}`}
        role="group"
        aria-label={label}
        style={style}
      >
        {children}
      </div>
      {/* after the tile, so that the knob lies on top of it and a tile that clips its content keeps it */}
      {CONTENT_SOURCES.has(kind) && (
        <>
          <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
          <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-tile" title="Drag to a console or group: its content goes there once" />
        </>
      )}
    </>
  );
}

interface TileHeadProps {
  id: string;
  /** An icon, or something of the same size: progress ring, avatar, app icon. */
  icon: IconName | ReactElement;
  /** Plain text, or an <InlineTitle>. */
  title: ReactNode;
  /** Small text or badges right after the title. */
  meta?: ReactNode;
  /** Buttons before the display button: size sm, ghost. */
  actions?: ReactNode;
  /** The card's own actions; the "…" menu goes on with what every card has. */
  menu?: MenuEntry[];
  menuLabel?: string;
  /** Replaces the plain Delete at the bottom, for a card that asks first or does more. */
  deleteItem?: MenuItem;
  /** The display button stays visible instead of showing on hover. */
  showDisplay?: boolean;
  hint?: string;
  className?: string;
}

/**
 * The header of every card (DESIGN.md §3.5): mark, title, meta, actions, display, "…", in that
 * order and at the same height everywhere, and the card is dragged by it. A console has its own
 * header component, on the same measures.
 */
export function TileHead({ id, icon, title, meta, actions, menu, menuLabel, deleteItem, showDisplay, hint, className }: TileHeadProps) {
  const { cardMenu } = useBoardEnv();
  const items = (): MenuEntry[] => [
    ...(menu?.length ? [...menu, 'sep' as const] : []),
    ...cardMenu(id, !deleteItem),
    ...(deleteItem ? ['sep' as const, deleteItem] : []),
  ];
  return (
    <div className={`tile-head ${className ?? ''}`} title={hint}>
      <span className="tile-mark" aria-hidden="true">
        {typeof icon === 'string' ? <Icon name={icon} /> : icon}
      </span>
      {typeof title === 'string' ? (
        <span className="tile-title" title={title}>
          {title}
        </span>
      ) : (
        title
      )}
      {meta}
      <span className="spacer" />
      {actions}
      <FocusButton id={id} />
      <DisplayButton id={id} always={showDisplay} />
      <Menu className="nodrag" label={menuLabel ?? (typeof title === 'string' ? `Actions for ${title}` : 'Card actions')} items={items} />
    </div>
  );
}

/**
 * Text that is edited locally and committed as the user types (merged into one undo
 * step). Remote changes don't overwrite the draft while editing.
 */
export function useTextDraft(id: string, key: string, value: string, editing: boolean) {
  const { ctl } = useBoardEnv();
  const [draft, setDraft] = useState(value);
  const editingRef = useRef(editing);
  useEffect(() => {
    if (editing && !editingRef.current) setDraft(value);
    editingRef.current = editing;
    if (!editing) setDraft(value);
  }, [editing, value]);
  const change = useCallback(
    (v: string) => {
      setDraft(v);
      ctl.updateData(id, { [key]: v }, { merge: `text:${id}` });
    },
    [ctl, id, key],
  );
  return { draft, change };
}

/** Largest font size (max → min px) at which the text fits the element. */
export function useFitText(ref: RefObject<HTMLElement | null>, deps: unknown[], max: number, min: number) {
  const [size, setSize] = useState(max);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    let lo = min;
    let hi = max;
    el.style.fontSize = `${hi}px`;
    if (el.scrollHeight <= el.clientHeight + 1) {
      setSize(hi);
      return;
    }
    while (hi - lo > 0.5) {
      const mid = (lo + hi) / 2;
      el.style.fontSize = `${mid}px`;
      if (el.scrollHeight <= el.clientHeight + 1) lo = mid;
      else hi = mid;
    }
    el.style.fontSize = `${lo}px`;
    setSize(Math.floor(lo));
  }, deps);
  return size;
}

interface InlineTitleProps {
  value: string;
  onCommit: (v: string) => void;
  label: string;
  className?: string;
  maxLength?: number;
  placeholder?: string;
}

/** Title that turns into an input on double-click (Enter saves, Esc cancels). */
export function InlineTitle({ value, onCommit, label, className, maxLength = 80, placeholder }: InlineTitleProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  const live = useRef(false);
  const start = () => {
    setDraft(value);
    live.current = true;
    setEditing(true);
  };
  const commit = () => {
    if (!live.current) return;
    live.current = false;
    setEditing(false);
    const v = draft.trim();
    if (v && v !== value) onCommit(v);
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    e.stopPropagation();
    if (e.key === 'Enter') commit();
    else if (e.key === 'Escape') {
      live.current = false;
      setEditing(false);
    }
  };
  if (editing) {
    return (
      <input
        className={`control control-sm tile-title-input nodrag ${className ?? ''}`}
        value={draft}
        autoFocus
        maxLength={maxLength}
        aria-label={label}
        placeholder={placeholder}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={onKey}
        onFocus={(e) => e.currentTarget.select()}
      />
    );
  }
  return (
    <span
      className={`tile-title ${className ?? ''}`}
      title={`${value} — double-click to rename`}
      onDoubleClick={(e) => {
        e.stopPropagation();
        start();
      }}
    >
      {value || placeholder}
    </span>
  );
}

/** Pointer drag inside a tile (kanban cards, calendar events): screen-space, works under zoom. */
export function usePointerDrag<T>(opts: {
  onStart?: (item: T) => void;
  onMove: (item: T, x: number, y: number) => void;
  onDrop: (item: T, x: number, y: number) => void;
  onCancel?: (item: T) => void;
  threshold?: number;
}) {
  const optsRef = useRef(opts);
  optsRef.current = opts;
  const [dragging, setDragging] = useState<T | null>(null);
  const begin = useCallback((e: ReactPointerEvent, item: T) => {
    if (e.button !== 0) return;
    const sx = e.clientX;
    const sy = e.clientY;
    let started = false;
    const th = optsRef.current.threshold ?? 5;
    const move = (ev: PointerEvent) => {
      if (!started) {
        if (Math.hypot(ev.clientX - sx, ev.clientY - sy) < th) return;
        started = true;
        setDragging(item);
        optsRef.current.onStart?.(item);
      }
      optsRef.current.onMove(item, ev.clientX, ev.clientY);
    };
    const up = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', cancel);
      window.removeEventListener('keydown', esc, true);
      if (started) {
        setDragging(null);
        optsRef.current.onDrop(item, ev.clientX, ev.clientY);
      }
    };
    const cancel = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', cancel);
      window.removeEventListener('keydown', esc, true);
      if (started) {
        setDragging(null);
        optsRef.current.onCancel?.(item);
      }
    };
    const esc = (ev: globalThis.KeyboardEvent) => {
      if (ev.key === 'Escape') {
        ev.stopPropagation();
        cancel();
      }
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', cancel);
    window.addEventListener('keydown', esc, true);
  }, []);
  return { dragging, begin };
}

/** Stop keyboard events inside tile inputs from reaching board shortcuts / React Flow. */
export const stopKeys = (e: KeyboardEvent) => e.stopPropagation();
