// Global toasts (DESIGN.md §3.6): bottom-right above the minimap, auto-dismiss
// (4 s, errors 7 s), role="status" / role="alert" for errors.

import { useSyncExternalStore } from 'react';
import { Button } from './Button';
import { Icon } from './Icon';

interface ToastAction {
  label: string;
  onClick: () => void;
}

interface ToastItem {
  id: number;
  kind: 'info' | 'error' | 'success' | 'warning';
  text: string;
  /** Bold first line (attention toasts). */
  title?: string;
  action?: ToastAction;
}

let items: ToastItem[] = [];
let nextId = 1;
const listeners = new Set<() => void>();

function emit() {
  listeners.forEach((l) => l());
}

function push(kind: ToastItem['kind'], text: string, ms: number, extra?: { title?: string; action?: ToastAction }) {
  const id = nextId++;
  items = [...items, { id, kind, text, ...extra }].slice(-4);
  emit();
  window.setTimeout(() => dismiss(id), ms);
  return id;
}

function dismiss(id: number) {
  items = items.filter((t) => t.id !== id);
  emit();
}

export const toast = {
  info: (text: string) => push('info', text, 4000),
  success: (text: string) => push('success', text, 4000),
  error: (text: string) => push('error', text, 7000),
  /** Toast with a title and an action button (attention events). */
  notify: (kind: ToastItem['kind'], title: string, text: string, action?: ToastAction) =>
    push(kind, text, kind === 'error' || kind === 'warning' ? 8000 : 6000, { title, action }),
  dismiss,
};

const ICONS = { info: 'info', success: 'check', error: 'alertCircle', warning: 'bell' } as const;

export function Toaster() {
  const list = useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => items,
  );
  return (
    <div className="toasts">
      {list.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`} role={t.kind === 'error' || t.kind === 'warning' ? 'alert' : 'status'}>
          <Icon name={ICONS[t.kind]} />
          <span className="toast-text">
            {t.title && <strong className="toast-title">{t.title}</strong>}
            {t.text}
          </span>
          {t.action && (
            <Button
              variant="secondary"
              size="sm"
              className="toast-action"
              onClick={() => {
                dismiss(t.id);
                t.action?.onClick();
              }}
            >
              {t.action.label}
            </Button>
          )}
          <Button variant="ghost" size="sm" iconOnly icon="close" aria-label="Dismiss notification" onClick={() => dismiss(t.id)} />
        </div>
      ))}
    </div>
  );
}
