// The popup that offers a newer release when the app opens (docs/API.md Hub update, DESIGN.md
// §3.4 Update reminder): to the owner only, once per opening while the hub says to ask. Update now
// installs it here and shows the progress; Skip this version never asks for that release again;
// Skip for now (and closing) asks again in 24 hours. The answer is kept by the hub, so every
// browser and device follows it.

import { useEffect, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { useAuth } from '../auth';
import { buildChannel } from '../lib/build';
import { Button } from './Button';
import { answerReminder, loadUpdateCheck, startUpdate, useHubUpdate } from './hubUpdate';
import { Modal } from './Modal';
import { toast } from './Toast';

/** How often an open app asks again, so a "Skip for now" that ran out is seen without a reload. */
const RECHECK_MS = 60 * 60_000;

export function UpdateReminder() {
  const { isAdmin, hub } = useAuth();
  const dev = !hub || buildChannel(hub) === 'dev';
  const { check, flow } = useHubUpdate();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState<'' | 'update' | 'skip' | 'later'>('');
  const [error, setError] = useState('');
  // the release this opening has asked about already: never twice in one opening
  const asked = useRef('');

  useEffect(() => {
    if (!isAdmin || dev) return;
    loadUpdateCheck();
    const again = () => document.visibilityState === 'visible' && loadUpdateCheck();
    const timer = window.setInterval(() => loadUpdateCheck(true), RECHECK_MS);
    document.addEventListener('visibilitychange', again);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', again);
    };
  }, [isAdmin, dev]);

  const latest = check?.latest ?? '';
  useEffect(() => {
    if (!isAdmin || dev || !check?.remind || !latest || flow.step !== 'idle') return;
    const key = `${latest}@${check.remindAfter ?? ''}`;
    if (asked.current === key) return;
    asked.current = key;
    setError('');
    setOpen(true);
  }, [isAdmin, dev, check?.remind, check?.remindAfter, latest, flow.step]);

  if (!open || !check || !latest) return null;

  const answer = async (action: 'skip' | 'later') => {
    setBusy(action);
    try {
      await answerReminder(action, latest);
      setOpen(false);
      if (action === 'skip') toast.info(`NextBoard ${latest} skipped. A newer release will ask again.`);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };
  const update = async () => {
    setBusy('update');
    setError('');
    try {
      await startUpdate(latest);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };
  // closing is "not now", unless the update is under way (it goes on; the header shows it)
  const close = () => (flow.step === 'idle' || flow.step === 'failed' ? void answer('later') : setOpen(false));

  const going = flow.step === 'running' || flow.step === 'restarting';
  const restarts = check.restarts ?? [];
  return (
    <Modal
      title={flow.step === 'updated' ? `Updated to ${flow.target}` : 'Update available'}
      icon="download"
      size="md"
      onClose={close}
      dismissable={!going}
      className="update-reminder"
      footer={
        flow.step === 'updated' ? (
          flow.reload ? (
            <Button variant="primary" icon="refresh" onClick={() => window.location.reload()} autoFocus>
              Reload the page
            </Button>
          ) : (
            <Button variant="primary" onClick={() => setOpen(false)} autoFocus>
              Done
            </Button>
          )
        ) : going ? (
          <Button variant="secondary" onClick={() => setOpen(false)}>
            Hide
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={() => void answer('skip')} loading={busy === 'skip'} disabled={!!busy}>
              Skip this version
            </Button>
            <Button variant="secondary" onClick={() => void answer('later')} loading={busy === 'later'} disabled={!!busy}>
              Skip for now
            </Button>
            {!check.blocked && (
              <Button variant="primary" icon="download" onClick={() => void update()} loading={busy === 'update'} disabled={!!busy} autoFocus>
                Update now
              </Button>
            )}
          </>
        )
      }
    >
      <div className="stack update-reminder-body">
        {flow.step === 'running' ? (
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
        ) : flow.step === 'restarting' ? (
          <p role="status">
            Restarting… Consoles reconnect by themselves.
            {flow.slow && (
              <span className="fs-xs text-3">
                {' '}
                {flow.restartError ? `The hub could not restart itself: ${flow.restartError}.` : `The hub has not come back as ${flow.target} yet.`} See <code>nextboard logs</code> where it
                runs.
              </span>
            )}
          </p>
        ) : flow.step === 'updated' ? (
          <p role="status">
            NextBoard runs {flow.target} now.{flow.reload ? ' Reload the page to use its new web app.' : ''}
          </p>
        ) : (
          <>
            <p>
              <strong>NextBoard {latest}</strong> is out. This hub runs {check.current}.{' '}
              {check.url && (
                <a href={check.url} target="_blank" rel="noreferrer">
                  What’s new
                </a>
              )}
            </p>
            {check.blocked ? (
              <p className="text-2">
                {check.blocked}
                {check.command && (
                  <>
                    {' '}
                    In a terminal: <code>{check.command}</code>
                  </>
                )}
              </p>
            ) : (
              <p className="text-2">
                Update now downloads it, checks its checksum, replaces the hub’s binary and restarts, in a few seconds. Consoles reconnect by themselves; projects, boards, settings and
                logins stay as they are.
                {restarts.length > 1 && ` The other instances on this machine restart with it: ${restarts.filter((r) => !r.self).map((r) => r.name).join(', ')}.`}
              </p>
            )}
            <p className="fs-xs text-3">Skip for now asks again in 24 hours. Skip this version waits for the next release.</p>
            {(error || flow.step === 'failed') && (
              <div className="alert" role="alert">
                {error || (flow.step === 'failed' ? flow.error : '')}
              </div>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}
