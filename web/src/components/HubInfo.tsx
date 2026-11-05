// "About this hub" popover: name, version, URLs.
// Rendered in a portal: inside the glass header (which has its own backdrop-filter) the
// popover's blur would only see the header, not the page behind it.

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useAuth } from '../auth';
import { Button } from './Button';
import { Icon } from './Icon';

export function HubInfoButton() {
  const { hub, me } = useAuth();
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (!open) return;
    const r = triggerRef.current?.getBoundingClientRect();
    if (r) setPos({ top: r.bottom + 12, right: Math.max(12, window.innerWidth - r.right) });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!popRef.current?.contains(t) && !triggerRef.current?.contains(t)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    const onResize = () => setOpen(false);
    document.addEventListener('mousedown', onDown, true);
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('mousedown', onDown, true);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', onResize);
    };
  }, [open]);

  return (
    <>
      <Button
        ref={triggerRef}
        variant="ghost"
        size="sm"
        iconOnly
        icon="info"
        className="hub-info-btn"
        aria-label="About this hub"
        title="About this hub"
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => setOpen((o) => !o)}
      />
      {open &&
        createPortal(
          <div
            ref={popRef}
            className="popover"
            role="dialog"
            aria-label="About this hub"
            style={pos ? { top: pos.top, right: pos.right } : { visibility: 'hidden' }}
          >
            <div className="popover-head">
              <span className="brand-mark" aria-hidden="true">
                <Icon name="terminal" />
              </span>
              <div className="grow">
                <h2 className="truncate">{hub?.name ?? 'NextBoard hub'}</h2>
                <span className="fs-xs text-3">NextBoard {hub?.version ?? ''}</span>
              </div>
            </div>
            {!hub ? (
              <p className="text-3">Loading…</p>
            ) : (
              <dl className="kv">
                <dt>Signed in as</dt>
                <dd>{me.kind === 'admin' ? 'Admin' : `Device “${me.deviceName}”`}</dd>
                <dt>Listening on</dt>
                <dd className="mono tabular">{hub.listen}</dd>
                {hub.urls?.length > 0 && (
                  <>
                    <dt>Addresses</dt>
                    <dd>
                      <ul className="plain url-list">
                        {hub.urls.map((u) => (
                          <li key={u} title={u}>
                            {u}
                          </li>
                        ))}
                      </ul>
                    </dd>
                  </>
                )}
              </dl>
            )}
          </div>,
          document.body,
        )}
    </>
  );
}
