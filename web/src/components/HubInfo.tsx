// "About this hub": name, which build runs (version, commit, build time, a newer release) and
// URLs. A popover from the version label in the desktop and tablet headers, a dialog from the
// "…" menu of the phone (and tablet) header. The label is how the owner sees at a glance which
// build runs; the panel is where a newer release is installed (docs/API.md Hub update).
// Rendered in a portal: inside the glass header (which has its own backdrop-filter) the
// popover's blur would only see the header, not the page behind it.

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useAuth } from '../auth';
import { buildChannel, buildLabel, commitHash, commitUrl, isDirty } from '../lib/build';
import { formatDateTime, prefs, timeAgo } from '../lib/format';
import type { Hub } from '../lib/types';
import { Button } from './Button';
import { ConfirmDialog } from './ConfirmDialog';
import { Icon } from './Icon';
import { loadUpdateCheck, startUpdate, useHubUpdate } from './hubUpdate';
import { Modal } from './Modal';

const LABEL_KEY = 'hubBuildLabel';

/**
 * The header's label ("v0.9.0 · 3f2a1c9"). Until /api/hub answers it shows what it showed last
 * time in this browser, so the header does not shift when the answer comes.
 */
export function useBuildLabel(): string {
  const { hub } = useAuth();
  const label = hub ? buildLabel(hub) : '';
  useEffect(() => {
    if (label) prefs.set(LABEL_KEY, label);
  }, [label]);
  return label || prefs.get(LABEL_KEY) || '';
}

/** Asks the hub for its cached check once it is known not to be a dev build (which asks nothing). */
function useUpdateCheck(hub: Hub | null) {
  const dev = !hub || buildChannel(hub) === 'dev';
  useEffect(() => {
    if (!dev) loadUpdateCheck();
  }, [dev]);
}

export function HubInfoButton() {
  const { hub } = useAuth();
  const label = useBuildLabel();
  const { check } = useHubUpdate();
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState('');
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  useUpdateCheck(hub);

  useLayoutEffect(() => {
    if (!open) return;
    const r = triggerRef.current?.getBoundingClientRect();
    if (r) setPos({ top: r.bottom + 12, right: Math.max(12, window.innerWidth - r.right) });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
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
    document.addEventListener('pointerdown', onDown, true);
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', onDown, true);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', onResize);
    };
  }, [open]);

  const available = check?.status === 'available' && !check.installed ? check.latest : '';
  const name = `About this hub: NextBoard ${label || 'version'}${available ? `, ${available} available` : ''}`;
  return (
    <>
      <Button
        ref={triggerRef}
        variant="ghost"
        size="sm"
        className="build-label"
        aria-label={name}
        title={name}
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => setOpen((o) => !o)}
      >
        {available && <span className="dot dot-info" aria-hidden="true" />}
        <span className="build-label-text">{label}</span>
      </Button>
      {open &&
        createPortal(
          <div
            ref={popRef}
            className="popover hub-pop"
            role="dialog"
            aria-label="About this hub"
            style={pos ? { top: pos.top, right: pos.right } : { visibility: 'hidden' }}
          >
            <HubInfoContent
              onAskUpdate={(target) => {
                setOpen(false);
                setConfirm(target);
              }}
            />
          </div>,
          document.body,
        )}
      {confirm && (
        <UpdateConfirm
          target={confirm}
          onClose={() => setConfirm('')}
          onStarted={() => {
            setConfirm('');
            setOpen(true); // to watch it go
          }}
        />
      )}
    </>
  );
}

function HubInfoContent({ onAskUpdate }: { onAskUpdate: (target: string) => void }) {
  const { hub, me } = useAuth();
  useUpdateCheck(hub);
  return (
    <>
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
        <dl className="kv hub-kv">
          <BuildRows hub={hub} />
          <dt>Update</dt>
          <dd>
            <UpdateRow hub={hub} onAskUpdate={onAskUpdate} />
          </dd>
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
    </>
  );
}

function BuildRows({ hub }: { hub: Hub }) {
  const channel = buildChannel(hub);
  const hash = commitHash(hub.commit);
  const url = commitUrl(hub.repo, hub.commit);
  return (
    <>
      <dt>Version</dt>
      <dd className="build-version">
        <span className="mono truncate" title={hub.version}>
          {hub.version}
        </span>
        <span className="badge" data-channel={channel}>
          {channel}
        </span>
      </dd>
      <dt>Commit</dt>
      <dd>
        {!hash ? (
          <span className="text-3">unknown</span>
        ) : url ? (
          <a href={url} target="_blank" rel="noreferrer" className="mono" title={`This commit on GitHub (${hub.repo})`}>
            {hash.slice(0, 12)}
          </a>
        ) : (
          <span className="mono">{hash.slice(0, 12)}</span>
        )}
        {isDirty(hub.commit) && <span className="text-3"> · with uncommitted changes</span>}
      </dd>
      <dt>Built</dt>
      <dd>
        {hub.builtAt ? (
          <>
            <span className="tabular">{formatDateTime(hub.builtAt)}</span>
            <span className="text-3"> · {timeAgo(hub.builtAt)}</span>
          </>
        ) : (
          <span className="text-3">unknown</span>
        )}
      </dd>
    </>
  );
}

function UpdateRow({ hub, onAskUpdate }: { hub: Hub; onAskUpdate: (target: string) => void }) {
  const { isAdmin } = useAuth();
  const { check, checkError, flow } = useHubUpdate();

  if (flow.step === 'running') {
    return (
      <div className="update-row" role="status">
        <span>
          {flow.text}
          {flow.pct != null && <span className="tabular"> {flow.pct}%</span>}
        </span>
        {flow.pct != null && (
          <span className="update-bar" aria-hidden="true">
            <span style={{ width: `${flow.pct}%` }} />
          </span>
        )}
      </div>
    );
  }
  if (flow.step === 'restarting') {
    return (
      <div className="update-row" role="status">
        <span>Restarting…</span>
        {flow.slow && (
          <span className="fs-xs text-3">
            {flow.restartError ? `The hub could not restart itself: ${flow.restartError}. ` : `The hub has not come back as ${flow.target} yet. `}
            See <code>nextboard logs</code> where it runs.
          </span>
        )}
      </div>
    );
  }
  if (flow.step === 'updated') {
    return (
      <div className="update-row" role="status">
        <span>Updated to {flow.target}</span>
        {flow.reload && (
          <Button size="sm" variant="secondary" icon="refresh" onClick={() => window.location.reload()}>
            Reload the page
          </Button>
        )}
      </div>
    );
  }
  if (buildChannel(hub) === 'dev') return <span className="text-2">dev build</span>;
  if (!check) return checkError ? <span className="text-3" title={checkError}>Could not check</span> : <span className="text-3">Checking…</span>;
  if (check.installed) {
    return (
      <div className="update-row">
        <span>{check.installed} is in place; the hub runs it once it restarts</span>
        {check.restartError && <span className="fs-xs text-danger">The restart failed: {check.restartError}</span>}
      </div>
    );
  }
  switch (check.status) {
    case 'current':
      return <span>Up to date</span>;
    case 'dev':
      return <span className="text-2">dev build</span>;
    case 'available':
      return (
        <div className="update-row">
          {check.url ? (
            <a href={check.url} target="_blank" rel="noreferrer">
              {check.latest} available
            </a>
          ) : (
            <span>{check.latest} available</span>
          )}
          {isAdmin && !check.blocked && check.latest && (
            <Button size="sm" variant="secondary" icon="download" onClick={() => onAskUpdate(check.latest!)}>
              Update to {check.latest}
            </Button>
          )}
          {flow.step === 'failed' && (
            <span className="fs-xs text-danger" role="alert">
              {flow.error}
            </span>
          )}
          <span className="fs-xs text-3">
            {check.blocked && `${check.blocked} `}
            {check.command && (
              <>
                In a terminal: <code>{check.command}</code>
              </>
            )}
          </span>
        </div>
      );
    default:
      return (
        <span className="text-3" title={check.error}>
          Could not check
        </span>
      );
  }
}

/** What the update does, before it does it. */
function UpdateConfirm({ target, onClose, onStarted }: { target: string; onClose: () => void; onStarted: () => void }) {
  return (
    <ConfirmDialog
      title={`Update to ${target}?`}
      icon="download"
      confirmLabel={`Update to ${target}`}
      onClose={onClose}
      onConfirm={async () => {
        await startUpdate(target);
        onStarted();
      }}
    >
      <ul className="update-steps">
        <li>The hub downloads {target} from GitHub, checks its SHA-256 against the release’s checksums, and replaces its binary. The current one stays next to it as a .old file.</li>
        <li>Then it restarts, which takes a few seconds. Consoles reconnect by themselves, and agents come back asleep or resume, as after any restart.</li>
        <li>Nothing else changes: projects, boards, settings, credentials and logins stay as they are.</li>
      </ul>
    </ConfirmDialog>
  );
}

export function AboutHubDialog({ onClose }: { onClose: () => void }) {
  const [confirm, setConfirm] = useState('');
  return (
    <>
      <Modal title="About this hub" icon="info" size="sm" onClose={onClose}>
        <div className="about-hub">
          <HubInfoContent onAskUpdate={setConfirm} />
        </div>
      </Modal>
      {confirm && <UpdateConfirm target={confirm} onClose={() => setConfirm('')} onStarted={() => setConfirm('')} />}
    </>
  );
}
