// Where a project's alerts go besides the board: a webhook reaches someone with no board open.

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { TextField } from '../components/form';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { formatDateTime } from '../lib/format';
import { uptimeApi, type UptimeSettings } from './api';
import { refreshUptimeSettings, setUptimeSettings } from './store';

export function AlertsDialog({ projectId, settings, onClose }: { projectId: string; settings: UptimeSettings; onClose: () => void }) {
  // the webhook as the hub has it now, until it is typed in; how the last delivery went is live
  const [typed, setTyped] = useState<string | null>(null);
  const url = typed ?? settings.webhook ?? '';
  const [busy, setBusy] = useState<'' | 'save' | 'test'>('');
  const [error, setError] = useState('');

  useEffect(() => {
    void refreshUptimeSettings(projectId);
  }, [projectId]);

  const save = async () => {
    const next = await uptimeApi.setWebhook(projectId, url.trim());
    setUptimeSettings(projectId, next);
  };
  const run = async (what: 'save' | 'test') => {
    setBusy(what);
    setError('');
    try {
      await save();
      if (what === 'test') {
        await uptimeApi.testWebhook(projectId);
        toast.success('Test message sent.');
      } else {
        onClose();
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };

  return (
    <Modal
      title="Uptime alerts"
      icon="bell"
      onClose={onClose}
      footer={
        <>
          <Button disabled={!url.trim() || !!busy} loading={busy === 'test'} onClick={() => void run('test')}>
            Send test
          </Button>
          <Button variant="primary" loading={busy === 'save'} disabled={!!busy} onClick={() => void run('save')}>
            Save
          </Button>
        </>
      }
    >
      <div className="up-form">
        <p className="up-form-note">Open boards show an alert anyway. A webhook also tells you when no board is open: when a monitor goes down, comes back, or its certificate is about to expire.</p>
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        <TextField
          label="Webhook URL"
          optional
          mono
          type="url"
          placeholder="https://hooks.slack.com/services/…"
          hint="Gets a JSON POST with the message in text and in content, the fields Slack, Telegram and Discord read. Kept on the hub as written."
          value={url}
          onChange={(e) => setTyped(e.target.value)}
        />
        {settings.lastError && (
          <p className="up-form-note" role="status">
            Last delivery failed{settings.lastErrorAt ? ` on ${formatDateTime(settings.lastErrorAt)}` : ''}: {settings.lastError}
          </p>
        )}
      </div>
    </Modal>
  );
}
