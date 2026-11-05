// The stash in the source control view: put changes away, and bring them back.

import { useEffect, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { formatDateTime } from '../../lib/format';
import { gitError } from '../../git/common';
import { gitApi } from '../api';
import type { GitStash } from '../types';
import { STASH_LABEL } from './CommitView';
import type { Workspace } from './workspace';

export function StashSection({ ws }: { ws: Workspace }) {
  const target = ws.gitTarget;
  const [items, setItems] = useState<GitStash[]>([]);
  const [open, setOpen] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [untracked, setUntracked] = useState(true);
  const [busy, setBusy] = useState(false);
  const [drop, setDrop] = useState<GitStash | null>(null);

  const load = () =>
    gitApi
      .stashes(target)
      .then(setItems)
      .catch(() => setItems([]));

  useEffect(() => {
    void load();
    // the list moves with a commit, a pull or a switch of repository
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, ws.gitVersion, target.repo]);

  const act = async (action: 'apply' | 'pop', s: GitStash) => {
    setBusy(true);
    try {
      await gitApi.stashAct(target, action, s.ref);
      toast.success(action === 'pop' ? 'Stash brought back and removed.' : 'Stash brought back.');
    } catch (err) {
      toast.error(gitError(err));
    } finally {
      setBusy(false);
      void ws.refreshGit();
      void load();
    }
  };

  const changed = !!ws.git?.files?.length;
  return (
    <div className="wb-stash">
      <div className="wb-section-title wb-history-head">
        <button type="button" className="wb-history-toggle" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
          <span>Stash</span>
        </button>
        {changed && (
          <button type="button" className="wb-section-act" title="Put the changes away for later" onClick={() => setSaving(true)}>
            Stash changes
          </button>
        )}
        <span className="wb-result-count tabular">{items.length}</span>
      </div>
      {open &&
        items.map((s) => (
          <div key={s.ref} className="wb-row wb-change wb-stash-row" title={`${s.message}\n${formatDateTime(s.at)}`} onClick={() => ws.openCommit(s.hash, { label: `${STASH_LABEL}${s.message}` })}>
            <Icon name="archive" className="wb-icon" />
            <span className="wb-row-name truncate">{s.message}</span>
            <button
              type="button"
              className="wb-result-act"
              aria-label="Apply this stash and keep it"
              title="Apply"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                void act('apply', s);
              }}
            >
              <Icon name="download" />
            </button>
            <button
              type="button"
              className="wb-result-act"
              aria-label="Apply this stash and remove it"
              title="Pop"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                void act('pop', s);
              }}
            >
              <Icon name="arrowUp" />
            </button>
            <button
              type="button"
              className="wb-result-act"
              aria-label="Delete this stash"
              title="Drop"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                setDrop(s);
              }}
            >
              <Icon name="close" />
            </button>
          </div>
        ))}
      {saving && (
        <Modal
          title="Stash changes"
          size="sm"
          onClose={() => !busy && setSaving(false)}
          footer={
            <>
              <Button variant="secondary" disabled={busy} onClick={() => setSaving(false)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                loading={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await ws.saveAll();
                    await gitApi.stashSave(target, message, untracked);
                    setSaving(false);
                    setMessage('');
                    toast.success('Changes stashed.');
                  } catch (err) {
                    toast.error(gitError(err));
                  } finally {
                    setBusy(false);
                    void ws.refreshGit();
                    void load();
                  }
                }}
              >
                Stash
              </Button>
            </>
          }
        >
          <label className="field">
            <span className="field-label">Message</span>
            <input className="control" aria-label="Stash message" value={message} autoFocus onChange={(e) => setMessage(e.target.value)} />
          </label>
          <label className="wb-check">
            <input type="checkbox" checked={untracked} onChange={(e) => setUntracked(e.target.checked)} />
            <span>Include new files</span>
          </label>
        </Modal>
      )}
      {drop && (
        <ConfirmDialog
          title="Delete this stash?"
          confirmLabel="Delete"
          danger
          onClose={() => setDrop(null)}
          onConfirm={async () => {
            try {
              await gitApi.stashAct(target, 'drop', drop.ref);
            } catch (err) {
              throw new Error(errorMessage(err));
            } finally {
              void load();
            }
          }}
        >
          <p>{drop.message}</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
