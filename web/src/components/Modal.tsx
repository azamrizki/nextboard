// Glass modal (DESIGN.md §3.6), rendered in a portal outside React Flow's canvas.
// Focus is trapped inside, Esc closes the topmost modal, focus returns to the trigger.
// On a phone it is a sheet from the bottom edge, and dragging its head down closes it. On a
// touch screen it does not focus a text field by itself, so the keyboard does not cover it.

import { useEffect, useId, useRef, type PointerEvent as ReactPointerEvent, type ReactNode, type SyntheticEvent } from 'react';
import { createPortal } from 'react-dom';
import { layoutState } from '../lib/layout';
import { Button } from './Button';
import { Icon, type IconName } from './Icon';

// Stack of open modals so only the topmost one reacts to Escape / Tab.
const stack: symbol[] = [];

const stop = (e: SyntheticEvent) => e.stopPropagation();

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

interface ModalProps {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  /** Action buttons, rendered right-aligned (Cancel first, primary last). */
  footer?: ReactNode;
  /** Short helper text on the left of the footer. */
  help?: ReactNode;
  size?: 'sm' | 'md' | 'lg';
  /** Icon before the title. */
  icon?: IconName;
  /** Controls in the head, before the close button. */
  actions?: ReactNode;
  /** Red title (destructive / security warnings). */
  tone?: 'default' | 'danger';
  /** Close when clicking the backdrop (default true). */
  dismissable?: boolean;
  className?: string;
}

export function Modal({
  title,
  onClose,
  children,
  footer,
  help,
  size = 'md',
  icon,
  actions,
  tone = 'default',
  dismissable = true,
  className,
}: ModalProps) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  // Captured on first render, before anything inside the modal takes focus.
  const prevFocus = useRef(document.activeElement as HTMLElement | null);

  useEffect(() => {
    const previouslyFocused = prevFocus.current;
    const el = ref.current;
    const token = Symbol('modal');
    stack.push(token);
    // Respect a child that already took focus (autoFocus), otherwise focus the first control.
    // A finger gets the dialog itself: a text field would open the keyboard over it.
    if (el && !el.contains(document.activeElement)) {
      const first =
        layoutState().input === 'touch'
          ? null
          : el.querySelector<HTMLElement>(
              '.modal-body input:not([type="hidden"]):not([disabled]), .modal-body select, .modal-body textarea, .modal-body [role="radio"][tabindex="0"], .modal-body button:not([disabled]), .modal-foot button:not([disabled])',
            );
      (first ?? el).focus();
    }

    const onKey = (e: KeyboardEvent) => {
      if (stack[stack.length - 1] !== token) return;
      if (e.key === 'Escape') {
        // an inline editor inside the modal handles its own Escape
        if ((e.target as HTMLElement | null)?.closest?.('[data-keep-escape]')) return;
        e.stopPropagation();
        onCloseRef.current();
      } else if (e.key === 'Tab' && el) {
        const items = Array.from(el.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
          (n) => n.offsetParent !== null || n === document.activeElement,
        );
        if (items.length === 0) return;
        const firstItem = items[0];
        const lastItem = items[items.length - 1];
        if (e.shiftKey && (document.activeElement === firstItem || !el.contains(document.activeElement))) {
          e.preventDefault();
          lastItem.focus();
        } else if (!e.shiftKey && (document.activeElement === lastItem || !el.contains(document.activeElement))) {
          e.preventDefault();
          firstItem.focus();
        }
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      const i = stack.indexOf(token);
      if (i >= 0) stack.splice(i, 1);
      previouslyFocused?.focus?.();
    };
  }, []);

  // Phone sheet: drag the head down to close it, like any sheet on the phone.
  const drag = useRef<{ id: number; y: number; dy: number } | null>(null);
  const onHeadDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (layoutState().layout !== 'phone' || e.pointerType === 'mouse' || (e.target as HTMLElement).closest('button')) return;
    drag.current = { id: e.pointerId, y: e.clientY, dy: 0 };
    e.currentTarget.setPointerCapture(e.pointerId);
    if (ref.current) ref.current.style.transition = 'none';
  };
  const onHeadMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId || !ref.current) return;
    d.dy = Math.max(0, e.clientY - d.y);
    ref.current.style.transform = d.dy ? `translateY(${d.dy}px)` : '';
  };
  const onHeadUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId) return;
    drag.current = null;
    if (ref.current) ref.current.style.transition = '';
    if (d.dy > 96) onClose();
    else if (ref.current) ref.current.style.transform = '';
  };

  return createPortal(
    <div
      className="modal-layer"
      onMouseDown={(e) => {
        if (dismissable && e.target === e.currentTarget) onClose();
      }}
      // React events bubble through portals to React ancestors (e.g. a React Flow node
      // or a form); keep them inside the modal.
      onKeyDown={stop}
      onClick={stop}
      onDoubleClick={stop}
      onPointerDown={stop}
      onSubmit={stop}
    >
      {/* Sibling (not parent) of the dialog: an ancestor with backdrop-filter would become
          the dialog's backdrop root and its own glass blur would no longer reach the page. */}
      <div className="modal-backdrop" aria-hidden="true" />
      <div
        ref={ref}
        className={`modal size-${size} ${className ?? ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
      >
        <div
          className={`modal-head ${tone === 'danger' ? 'danger' : ''}`}
          onPointerDown={onHeadDown}
          onPointerMove={onHeadMove}
          onPointerUp={onHeadUp}
          onPointerCancel={onHeadUp}
        >
          <h2 id={titleId}>
            {icon && <Icon name={icon} />}
            <span className="truncate">{title}</span>
          </h2>
          {actions && <div className="modal-head-actions">{actions}</div>}
          <Button variant="ghost" size="sm" iconOnly icon="close" aria-label="Close dialog" onClick={onClose} />
        </div>
        <div className="modal-body">{children}</div>
        {(footer || help) && (
          <div className="modal-foot">
            {help && <div className="foot-help">{help}</div>}
            {footer && <div className="foot-actions">{footer}</div>}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}
