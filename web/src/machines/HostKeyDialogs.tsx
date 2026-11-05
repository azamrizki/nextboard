// Host key verification: a calm TOFU confirmation for unknown hosts, and an unmistakable
// red "host key changed" warning (no accept button) when a known host's key differs.

import { useState } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { Machine } from '../lib/types';
import { store } from '../store';

function hostLabel(m: Machine) {
  if (m.pendingHostKey?.host) return m.pendingHostKey.host;
  const host = m.host || m.sshConfigHost || m.name;
  return m.port && m.port !== 22 ? `${host}:${m.port}` : host;
}

/** Red panel used both in the dialog and inline on the machine card. */
export function HostKeyMismatchWarning({ machine }: { machine: Machine }) {
  return (
    <div className="danger-panel" role="alert">
      <Icon name="shieldAlert" />
      <div>
        <h4>Host key changed</h4>
        <p>
          The SSH host key for <code>{hostLabel(machine)}</code> doesn’t match the one recorded earlier. Connection
          refused — someone may be intercepting the connection, or the server was reinstalled.
        </p>
        <p>
          If the change is expected, remove the old entry for this host from <code>~/.ssh/known_hosts</code> and{' '}
          <code>~/.nextboard/known_hosts</code> on the hub, then press Connect again.
        </p>
      </div>
    </div>
  );
}

export function HostKeyMismatchDialog({ machine, onClose }: { machine: Machine; onClose: () => void }) {
  return (
    <Modal
      title={`Connection refused: ${machine.name}`}
      icon="shieldAlert"
      tone="danger"
      onClose={onClose}
      size="md"
      help="NextBoard never accepts a changed host key automatically."
      footer={
        <Button variant="secondary" onClick={onClose} autoFocus>
          Close
        </Button>
      }
    >
      <HostKeyMismatchWarning machine={machine} />
    </Modal>
  );
}

interface HostKeyDialogProps {
  machine: Machine;
  onClose: () => void;
  /** The host is trusted now, and asks for a password or passphrase next. */
  onNeedsAuth?: (m: Machine) => void;
}

/** Trust-on-first-use confirmation for machine.pendingHostKey. */
export function HostKeyDialog({ machine, onClose, onNeedsAuth }: HostKeyDialogProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const pending = machine.pendingHostKey;

  if (machine.hostKeyMismatch) return <HostKeyMismatchDialog machine={machine} onClose={onClose} />;

  const accept = async () => {
    if (!pending) return;
    setBusy(true);
    setError('');
    try {
      const m = await api.acceptHostKey(machine.id, pending.fingerprint);
      store.upsertMachine(m);
      if (m.needsAuth) onNeedsAuth?.(m);
      else if (m.status === 'connected') toast.success(`Connected to ${m.name}.`);
      else if (m.status === 'error') toast.error(`${m.name}: ${m.error || 'connection failed after trusting the key.'}`);
      onClose();
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 409
          ? 'The host key changed while this dialog was open. Close it and press Connect again.'
          : errorMessage(err),
      );
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`Trust ${machine.name}?`}
      icon="shield"
      onClose={() => !busy && onClose()}
      size="md"
      dismissable={!busy}
      help="Saved to ~/.nextboard/known_hosts once trusted."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon="check" onClick={accept} loading={busy} disabled={!pending}>
            Trust and connect
          </Button>
        </>
      }
    >
      {!pending ? (
        <p className="text-2">No host key is waiting for confirmation.</p>
      ) : (
        <div className="stack">
          <p className="text-2">
            This is the first connection to this host. Compare the fingerprint with the server’s own (
            <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code>) before trusting it.
          </p>
          <div className="panel">
            <dl className="kv">
              <dt>Host</dt>
              <dd className="mono">{pending.host}</dd>
              <dt>Key type</dt>
              <dd className="mono">{pending.keyType}</dd>
              <dt>Fingerprint</dt>
              <dd className="hostkey-fp">{pending.fingerprint}</dd>
            </dl>
          </div>
        </div>
      )}
      {error && (
        <div className="alert mt-4" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
    </Modal>
  );
}
