// Anchored popover (DESIGN.md §3.6: glass-3, radius lg): a portal under (or above) its
// trigger, closed by Esc / outside pointer / wheel; focus moves in and back to the trigger.

import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject, type SyntheticEvent } from 'react';
import { createPortal } from 'react-dom';

const stop = (e: SyntheticEvent) => e.stopPropagation();

interface PopoverProps {
  anchor: RefObject<HTMLElement | null>;
  onClose: () => void;
  label: string;
  children: ReactNode;
  className?: string;
  /** Preferred side; flips when there is no room. */
  placement?: 'below' | 'above';
  align?: 'start' | 'end';
  width?: number;
}

export function Popover({ anchor, onClose, label, children, className, placement = 'below', align = 'end', width }: PopoverProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useLayoutEffect(() => {
    const a = anchor.current?.getBoundingClientRect();
    const el = ref.current;
    if (!a || !el) return;
    const gap = 6;
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    let top = placement === 'below' ? a.bottom + gap : a.top - gap - h;
    if (placement === 'below' && top + h > window.innerHeight - gap && a.top - gap - h > gap) top = a.top - gap - h;
    if (placement === 'above' && top < gap && a.bottom + gap + h < window.innerHeight) top = a.bottom + gap;
    let left = align === 'end' ? a.right - w : a.left;
    left = Math.max(gap, Math.min(left, window.innerWidth - w - gap));
    top = Math.max(gap, Math.min(top, window.innerHeight - h - gap));
    setPos({ top, left });
  }, [anchor, placement, align]);

  useEffect(() => {
    if (!pos) return;
    const first = ref.current?.querySelector<HTMLElement>('button:not(:disabled), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
    (first ?? ref.current)?.focus();
  }, [pos]);

  useEffect(() => {
    const trigger = anchor.current;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || trigger?.contains(t)) return;
      if ((t as HTMLElement).closest?.('.modal-layer, .menu-list')) return;
      closeRef.current();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer')) {
        e.stopPropagation();
        closeRef.current();
        trigger?.focus();
      }
    };
    const onWheel = (e: WheelEvent) => {
      if (!ref.current?.contains(e.target as Node)) closeRef.current();
    };
    document.addEventListener('mousedown', onDown, true);
    document.addEventListener('keydown', onKey, true);
    document.addEventListener('wheel', onWheel, { capture: true, passive: true });
    return () => {
      document.removeEventListener('mousedown', onDown, true);
      document.removeEventListener('keydown', onKey, true);
      document.removeEventListener('wheel', onWheel, true);
    };
  }, [anchor]);

  return createPortal(
    <div
      ref={ref}
      className={`popover ${className ?? ''}`}
      role="dialog"
      aria-label={label}
      tabIndex={-1}
      style={pos ? { top: pos.top, left: pos.left, width } : { visibility: 'hidden', top: 0, left: 0, width }}
      onClick={stop}
      onMouseDown={stop}
      onPointerDown={stop}
      onDoubleClick={stop}
      onKeyDown={stop}
      onContextMenu={stop}
    >
      {children}
    </div>,
    document.body,
  );
}
