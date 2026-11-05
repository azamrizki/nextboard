// Devices: paired apps (desktop / mobile) as bento cards. Pair, rename and revoke are admin-only.

import { useEffect, useRef, useState, type SyntheticEvent } from 'react';
import { api, errorMessage } from '../api';
import { useAuth } from '../auth';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon, type IconName } from '../components/Icon';
import { StatusBadge } from '../components/StatusDot';
import { toast } from '../components/Toast';
import { PairDialog } from '../devices/PairDialog';
import { formatDateTime, timeAgo } from '../lib/format';
import type { Device } from '../lib/types';
import { store, useDevices } from '../store';

const PLATFORM_ICON: Record<string, IconName> = { android: 'phone', ios: 'phone', desktop: 'monitor', web: 'monitor' };
const PLATFORM_LABEL: Record<string, string> = { android: 'Android', ios: 'iOS', desktop: 'Desktop', web: 'Web' };

export function DevicesPage() {
  const devices = useDevices();
  const { isAdmin, me } = useAuth();
  const [pairing, setPairing] = useState(false);
  const [revoking, setRevoking] = useState<Device | null>(null);
  const [loadError, setLoadError] = useState('');
  const [, setTick] = useState(0);

  useEffect(() => {
    api
      .devices()
      .then((list) => store.seedDevices(list))
      .catch((err) => setLoadError(errorMessage(err)));
    // Re-render periodically so "last seen" stays fresh.
    const t = window.setInterval(() => setTick((n) => n + 1), 30_000);
    return () => window.clearInterval(t);
  }, []);

  const selfId = me.kind === 'device' ? me.deviceId : '';

  return (
    <div className="page">
      <AppHeader />
      <main className="content">
        <div className="page-head">
          <div>
            <h1>Devices</h1>
            <p>Desktop and mobile apps paired with this hub. Browsers sign in with the admin password instead.</p>
          </div>
        </div>

        {loadError && (
          <div className="alert mb-4" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load devices: {loadError}</span>
          </div>
        )}

        <div className="bento">
          {isAdmin && (
            <button type="button" className="card card-clickable pair-card span-2" onClick={() => setPairing(true)}>
              <div className="card-head">
                <span className="card-icon" aria-hidden="true">
                  <Icon name="qr" />
                </span>
                <div className="card-title">
                  <h3>Pair new device</h3>
                  <span className="card-meta">Show a QR code and a one-time code for the NextBoard app. Valid for 5 minutes.</span>
                </div>
                <Icon name="plus" className="text-3" width={20} height={20} />
              </div>
            </button>
          )}

          {devices.map((d) => (
            <DeviceCard key={d.id} device={d} isAdmin={isAdmin} isSelf={d.id === selfId} onRevoke={() => setRevoking(d)} />
          ))}

          {devices.length === 0 && (
            <div className="card">
              <div className="empty-state">
                <span className="empty-icon">
                  <Icon name="phone" />
                </span>
                <h3>No devices yet</h3>
                <p>{isAdmin ? 'Pair the desktop or Android app to watch your agents from anywhere.' : 'No paired devices.'}</p>
              </div>
            </div>
          )}
        </div>
      </main>

      {pairing && (
        <PairDialog
          onClose={() => setPairing(false)}
          onPaired={(d) => {
            setPairing(false);
            toast.success(`Paired “${d.name}” (${PLATFORM_LABEL[d.platform] ?? d.platform}).`);
          }}
        />
      )}

      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          confirmLabel="Revoke device"
          danger
          onClose={() => setRevoking(null)}
          onConfirm={async () => {
            await api.revokeDevice(revoking.id);
            store.removeDevice(revoking.id);
          }}
        >
          <p>The device is disconnected immediately and can’t sign in again until it’s paired anew.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

interface DeviceCardProps {
  device: Device;
  isAdmin: boolean;
  isSelf: boolean;
  onRevoke: () => void;
}

function DeviceCard({ device: d, isAdmin, isSelf, onRevoke }: DeviceCardProps) {
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(d.name);
  const renamingRef = useRef(false);

  const start = () => {
    setDraft(d.name);
    renamingRef.current = true;
    setRenaming(true);
  };

  const commit = async (e: SyntheticEvent) => {
    e.preventDefault();
    if (!renamingRef.current) return;
    renamingRef.current = false;
    setRenaming(false);
    const name = draft.trim();
    if (!name || name === d.name) return;
    try {
      store.upsertDevice(await api.renameDevice(d.id, name));
    } catch (err) {
      toast.error(`Could not rename ${d.name}: ${errorMessage(err)}`);
    }
  };

  const platform = PLATFORM_LABEL[d.platform] ?? (d.platform || 'Unknown');

  return (
    <article className="card" aria-label={d.name}>
      <header className="card-head">
        <span className="card-icon" aria-hidden="true">
          <Icon name={PLATFORM_ICON[d.platform] ?? 'phone'} />
        </span>
        <div className="card-title">
          {renaming ? (
            <form onSubmit={commit}>
              <input
                className="control control-sm"
                value={draft}
                autoFocus
                onChange={(e) => setDraft(e.target.value)}
                onBlur={commit}
                onKeyDown={(e) => {
                  if (e.key === 'Escape') {
                    renamingRef.current = false;
                    setRenaming(false);
                  }
                }}
                aria-label="Device name"
                maxLength={64}
              />
            </form>
          ) : (
            <h3 className="truncate" title={d.name}>
              {d.name}
            </h3>
          )}
          <span className="card-meta">
            {platform}
            {isSelf ? ' · this device' : ''}
          </span>
        </div>
        <StatusBadge color={d.online ? 'success' : 'grey'} label={d.online ? 'Online' : 'Offline'} />
      </header>

      <dl className="kv">
        <dt>Paired</dt>
        <dd className="tabular">{formatDateTime(d.createdAt)}</dd>
        <dt>Last seen</dt>
        <dd className="tabular" title={formatDateTime(d.lastSeen)}>
          {d.online ? 'Now' : timeAgo(d.lastSeen)}
        </dd>
      </dl>

      {isAdmin && (
        <footer className="card-actions">
          <Button size="sm" icon="pencil" onClick={start}>
            Rename
          </Button>
          <span className="spacer" />
          <Button size="sm" variant="danger-ghost" icon="trash" onClick={onRevoke}>
            Revoke
          </Button>
        </footer>
      )}
    </article>
  );
}
