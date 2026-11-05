// Open pull requests in the source control view, and making a new one.

import { useEffect, useState } from 'react';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { gitError } from '../../git/common';
import { isCode } from '../../api';
import { gitApi } from '../api';
import type { GitPullSummary } from '../types';
import type { Workspace } from './workspace';

export function PullSection({ ws }: { ws: Workspace }) {
  const target = ws.gitTarget;
  const st = ws.git;
  const [items, setItems] = useState<GitPullSummary[] | null>(null);
  const [problem, setProblem] = useState('');
  const [hidden, setHidden] = useState(false);
  const [open, setOpen] = useState(true);
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState('');
  const [body, setBody] = useState('');
  const [base, setBase] = useState('');
  const [draft, setDraft] = useState(false);
  const [busy, setBusy] = useState(false);
  const github = !!st?.hasRemote && /github/i.test(`${st.webUrl} ${st.remoteUrl}`);

  const load = () =>
    gitApi
      .pulls(target)
      .then((list) => {
        setItems(list);
        setProblem('');
        setHidden(false);
      })
      .catch((err) => {
        setItems([]);
        // no gh, or a remote that is not GitHub: nothing to offer, and nothing to complain about
        setHidden(isCode(err, 'gh_missing') || isCode(err, 'no_github_remote'));
        setProblem(gitError(err));
      });

  useEffect(() => {
    if (github) void load();
    // reloads after a push or a switch of branch
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, github, target.repo, st?.branch, st?.ahead, ws.pullsTick]);

  if (!github || hidden) return null;
  const mine = items?.find((p) => p.headRefName === st?.branch);
  return (
    <div className="wb-pulls">
      <div className="wb-section-title wb-history-head">
        <button type="button" className="wb-history-toggle" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
          <span>Pull requests</span>
        </button>
        {!mine && st?.branch && (
          <button
            type="button"
            className="wb-section-act"
            title={`Open a pull request for ${st.branch}`}
            onClick={() => {
              setTitle('');
              setBody('');
              setBase('');
              setDraft(false);
              setCreating(true);
            }}
          >
            Create
          </button>
        )}
        <span className="wb-result-count tabular">{items?.length ?? 0}</span>
      </div>
      {open && problem && <div className="wb-row-msg">{problem}</div>}
      {open &&
        items?.map((p) => (
          <button
            key={p.number}
            type="button"
            className={`wb-row wb-pull ${ws.activeTab?.view?.kind === 'pr' && ws.activeTab.view.number === p.number ? 'is-selected' : ''}`}
            title={`${p.title}\n#${p.number} · ${p.author.login} · ${p.headRefName} → ${p.baseRefName}`}
            onClick={() => ws.openView({ kind: 'pr', number: p.number, title: p.title, repo: target.repo })}
            onDoubleClick={() => ws.openView({ kind: 'pr', number: p.number, title: p.title, repo: target.repo }, { pin: true })}
          >
            <Icon name="branch" className="wb-icon" />
            <span className="wb-row-name truncate">{p.title}</span>
            {p.isDraft && <span className="wb-pr-pill is-draft">draft</span>}
            <span className="wb-result-dir tabular">#{p.number}</span>
          </button>
        ))}
      {creating && (
        <Modal
          title={`Open a pull request for ${st?.branch}`}
          onClose={() => !busy && setCreating(false)}
          footer={
            <>
              <Button variant="secondary" disabled={busy} onClick={() => setCreating(false)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                loading={busy}
                disabled={!title.trim()}
                onClick={async () => {
                  setBusy(true);
                  try {
                    const r = await gitApi.pullCreate(target, { title: title.trim(), body, base: base.trim(), draft });
                    toast.success(`Opened ${r.url}`);
                    setCreating(false);
                    void load();
                    void ws.refreshGit();
                  } catch (err) {
                    toast.error(gitError(err));
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                Push and open
              </Button>
            </>
          }
        >
          <label className="field">
            <span className="field-label">Title</span>
            <input className="control" aria-label="Title" value={title} autoFocus onChange={(e) => setTitle(e.target.value)} />
          </label>
          <label className="field">
            <span className="field-label">Description</span>
            <textarea className="control prose" aria-label="Description" rows={5} value={body} onChange={(e) => setBody(e.target.value)} />
          </label>
          <label className="field">
            <span className="field-label">Into branch (empty: the default one)</span>
            <input className="control mono" aria-label="Into branch" spellCheck={false} value={base} onChange={(e) => setBase(e.target.value)} />
          </label>
          <label className="wb-check">
            <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} />
            <span>Draft</span>
          </label>
          <p className="text-3">The branch is pushed to origin first. Others can see the pull request on GitHub.</p>
        </Modal>
      )}
    </div>
  );
}
