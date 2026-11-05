// In-app replacement for window.confirm with async action, loading state and error display.

import { useState, type ReactNode } from 'react';
import { errorMessage } from '../api';
import { Button } from './Button';
import { Icon, type IconName } from './Icon';
import { Modal } from './Modal';

interface ConfirmDialogProps {
  title: ReactNode;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  icon?: IconName;
  /** May throw; the error is shown inside the dialog and it stays open. */
  onConfirm: () => Promise<void> | void;
  onClose: () => void;
}

export function ConfirmDialog({ title, children, confirmLabel, danger, icon, onConfirm, onClose }: ConfirmDialogProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const confirm = async () => {
    setBusy(true);
    setError('');
    try {
      await onConfirm();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={title}
      icon={icon ?? (danger ? 'alert' : undefined)}
      tone={danger ? 'danger' : 'default'}
      onClose={() => !busy && onClose()}
      size="sm"
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={confirm} loading={busy} autoFocus={!danger}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="stack">
        <div className="text-2">{children}</div>
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </Modal>
  );
}
