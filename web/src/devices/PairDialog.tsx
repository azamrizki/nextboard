// "Pair new device": QR + code + countdown + hub URLs.
// Closes itself as soon as a new device shows up on the events stream.

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { QR } from '../components/QR';
import { formatCountdown } from '../lib/format';
import type { Device, PairStart } from '../lib/types';
import { store } from '../store';

interface PairDialogProps {
  onClose: () => void;
  onPaired: (d: Device) => void;
}

export function PairDialog({ onClose, onPaired }: PairDialogProps) {
  const [pair, setPair] = useState<PairStart | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  // Devices that existed when the dialog opened; anything else is "the new one".
  const known = useRef(new Set(Object.keys(store.getState().devices)));
  const onPairedRef = useRef(onPaired);
  onPairedRef.current = onPaired;

  const start = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setPair(await api.pairStart());
      setNow(Date.now());
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void start();
  }, [start]);

  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);

  useEffect(
    () =>
      store.onEvent((ev) => {
        if (ev.type === 'device' && !known.current.has(ev.device.id)) {
          known.current.add(ev.device.id);
          onPairedRef.current(ev.device);
        }
      }),
    [],
  );

  const remaining = pair ? new Date(pair.expiresAt).getTime() - now : 0;
  const expired = !!pair && remaining <= 0;

  return (
    <Modal
      title="Pair new device"
      icon="qr"
      onClose={onClose}
      size="lg"
      help={
        pair && !expired ? (
          <span className="status">
            <span className="spinner" aria-hidden="true" />
            Waiting for the app…
          </span>
        ) : undefined
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Close
          </Button>
          <Button icon="refresh" onClick={() => void start()} loading={loading}>
            New code
          </Button>
        </>
      }
    >
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {!pair && !error && (
        <p className="status">
          <span className="spinner" aria-hidden="true" /> Generating a pairing code…
        </p>
      )}
      {pair && (
        <div className="pair">
          <div className={`pair-qr ${expired ? 'expired' : ''}`}>
            <QR value={pair.uri} size={216} />
            {expired && (
              <div className="pair-expired">
                <span>Code expired</span>
                <Button variant="primary" size="sm" icon="refresh" onClick={() => void start()} loading={loading}>
                  New code
                </Button>
              </div>
            )}
          </div>
          <div className="pair-info">
            <div>
              <p className="fs-sm text-2">Scan with the NextBoard app, or type this code:</p>
              <div className="pair-code" aria-label={`Pairing code ${pair.code.split('').join(' ')}`}>
                {pair.code}
              </div>
              <p aria-live="polite" className="fs-xs">
                {expired ? (
                  <span className="badge badge-danger">Expired</span>
                ) : (
                  <span className={`badge tabular ${remaining < 60_000 ? 'badge-warning' : 'badge-info'}`}>
                    Expires in {formatCountdown(remaining)}
                  </span>
                )}{' '}
                <span className="text-3">· single use</span>
              </p>
            </div>
            <dl className="kv">
              <dt>Hub</dt>
              <dd>{pair.hubName}</dd>
              <dt>Addresses</dt>
              <dd>
                {pair.urls?.length ? (
                  <ul className="plain url-list">
                    {pair.urls.map((u) => (
                      <li key={u} title={u}>
                        {u}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <span className="text-3">None detected</span>
                )}
              </dd>
            </dl>
            {!pair.urls?.some((u) => u.startsWith('https://')) && (
              <p className="field-hint">
                No HTTPS address yet. The Android release app only connects over HTTPS, so pair from this page through
                your HTTPS reverse proxy, or add its URL to <code>extraUrls</code> in the hub’s config.json.
              </p>
            )}
          </div>
        </div>
      )}
    </Modal>
  );
}
