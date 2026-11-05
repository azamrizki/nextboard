// A project's public status page: an address anyone can open with no login. What it may show
// is decided on the hub (internal/uptime/public.go); this says so before it is turned on.

import { useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Checkbox, TextField } from '../components/form';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { uptimeApi, type PublicPageSettings, type UptimeSettings } from './api';
import { setUptimeSettings } from './store';

export function PublicPageDialog({ projectId, settings, publicCount, onClose }: { projectId: string; settings: UptimeSettings; publicCount: number; onClose: () => void }) {
  const [page, setPage] = useState<PublicPageSettings>(settings.publicPage ?? { enabled: false });
  const [enabled, setEnabled] = useState(!!page.enabled);
  const [title, setTitle] = useState(page.title ?? '');
  const [busy, setBusy] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [error, setError] = useState('');
  const link = page.enabled && page.token ? `${window.location.origin}/status/${page.token}` : '';

  const keep = (next: PublicPageSettings) => {
    setPage(next);
    setUptimeSettings(projectId, { ...settings, publicPage: next });
  };

  const save = async () => {
    setBusy(true);
    setError('');
    try {
      keep(await uptimeApi.setPublicPage(projectId, { enabled, title: title.trim() }));
      toast.success(enabled ? 'The public page is on.' : 'The public page is off.');
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const rotate = async () => {
    keep(await uptimeApi.rotatePublicPage(projectId));
    toast.success('New address made. The old one no longer works.');
  };

  return (
    <Modal
      title="Public status page"
      icon="globe"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Close</Button>
          <Button variant="primary" loading={busy} disabled={enabled === page.enabled && title.trim() === (page.title ?? '')} onClick={() => void save()}>
            Save
          </Button>
        </>
      }
    >
      <div className="up-form">
        <p className="up-form-note">
          Anyone with the address can open it, with no login. It shows only the title below and, for each monitor you marked public, the name you chose for it, up or down, uptime and outage times. Addresses, hosts, ports, headers, error messages, response times and the name of this project are never on it.
        </p>
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        <Checkbox checked={enabled} onChange={setEnabled} label="Turn on the public page" />
        <TextField label="Title" optional placeholder="Status" value={title} maxLength={80} onChange={(e) => setTitle(e.target.value)} />
        <p className="up-form-note" role="status">
          {publicCount === 0 ? 'No monitor is marked public yet: edit a monitor and tick "Show on the public status page".' : `${publicCount === 1 ? '1 monitor is' : `${publicCount} monitors are`} on the page.`}
        </p>
        {link && (
          <>
            <TextField label="Address" mono readOnly value={link} onFocus={(e) => e.target.select()} />
            <div className="row-end">
              <Button size="sm" variant="ghost" onClick={() => setRotating(true)}>
                New address
              </Button>
              <Button
                size="sm"
                icon="copy"
                onClick={() => {
                  void navigator.clipboard?.writeText(link).then(
                    () => toast.success('Address copied.'),
                    () => toast.info('Select the address and copy it.'),
                  );
                }}
              >
                Copy
              </Button>
              <Button size="sm" onClick={() => window.open(link, '_blank', 'noopener,noreferrer')}>
                Open
              </Button>
            </div>
            <p className="up-form-note">
              This is this hub's own address, which only reaches who can already reach the hub. To put the page on the internet, do not expose the hub itself: start it with --status-listen and expose only that port. It serves this page and nothing else. See docs/API-uptime.md.
            </p>
          </>
        )}
      </div>
      {rotating && (
        <ConfirmDialog
          title="Make a new address?"
          confirmLabel="New address"
          onConfirm={rotate}
          onClose={() => setRotating(false)}
        >
          The address you gave out stops working at once.
        </ConfirmDialog>
      )}
    </Modal>
  );
}
