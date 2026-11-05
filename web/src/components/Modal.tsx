// Glass modal (DESIGN.md §3.6), rendered in a portal outside React Flow's canvas.
// Focus is trapped inside, Esc closes the topmost modal, focus returns to the trigger.

import { useEffect, useId, useRef, type ReactNode, type SyntheticEvent } from 'react';
import { createPortal } from 'react-dom';
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
    if (el && !el.contains(document.activeElement)) {
      const first = el.querySelector<HTMLElement>(
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
        <div className={`modal-head ${tone === 'danger' ? 'danger' : ''}`}>
          <h2 id={titleId}>
            {icon && <Icon name={icon} />}
            <span className="truncate">{title}</span>
          </h2>
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
