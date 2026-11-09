// What each agent changed and nobody has looked at yet (docs/API-studio.md §7.10).

import { useEffect, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { splitPath, STATUS_LABEL, STATUS_LETTER, STATUS_TONE } from '../../git/common';
import { reviewApi } from '../api';
import type { ReviewAgent } from '../types';
import { FileIcon } from './Explorer';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

export function ReviewPanel({ ws }: { ws: Workspace }) {
  useWs(ws);
  const [undo, setUndo] = useState<ReviewAgent | null>(null);
  const [busy, setBusy] = useState('');
  const s = ws.review;

  useEffect(() => {
    void ws.refreshReview();
  }, [ws]);

  const acceptAll = async (a: ReviewAgent) => {
    setBusy(a.sessionId);
    try {
      await reviewApi.accept(ws.pid, { session: a.sessionId });
      ws.closeReviewOf(a.sessionId);
      toast.success(`Kept what ${a.name} changed.`);
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy('');
      void ws.refreshReview();
    }
  };

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2 className="truncate">Review</h2>
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh" title="Refresh" onClick={() => void ws.refreshReview()} />
      </header>
      <div className="wb-tree nowheel">
        {!s && <div className="wb-row-msg">Loading…</div>}
        {s && !s.supported && <div className="wb-row-msg">A project on a whole home folder takes no part in review: it would copy every login and other project.</div>}
        {s?.problem && (
          <div className="wb-dbg-notice" role="alert">
            <p>{s.problem}</p>
          </div>
        )}
        {s?.supported && !s.agents.length && <div className="wb-row-msg">Nothing to review. What agents change shows up here, by agent.</div>}
        {s?.agents.map((a) => (
          <section key={a.sessionId} className="wb-rv-agent" aria-label={`Changes by ${a.name}`}>
            <div className="wb-section-title wb-rv-head">
              {a.working && <span className="dot dot-success" title={`${a.name} is at work`} aria-hidden="true" />}
              <span className="truncate" title={`${a.name} · ${a.kind}`}>
                {a.name}
              </span>
              <span className="wb-result-count tabular">{a.files.length}</span>
              <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => setUndo(a)}>
                Undo all
              </Button>
              <Button size="sm" variant="ghost" disabled={!!busy} loading={busy === a.sessionId} onClick={() => void acceptAll(a)}>
                Keep all
              </Button>
            </div>
            {a.files.map((f) => {
              const { dir, name } = splitPath(f.path);
              const also = f.also?.length ? ` Also changed by ${f.also.join(', ')}.` : '';
              const guess = f.unsure ? ' Several agents were at work: this may be another one’s.' : '';
              return (
                <button
                  key={f.path}
                  type="button"
                  className={`wb-row wb-change wb-rv-file git-${STATUS_TONE[f.status] ?? 'modified'} ${ws.activeTab?.view?.kind === 'review' && ws.activeTab.view.session === a.sessionId && ws.activeTab.view.path === f.path ? 'is-selected' : ''}`}
                  title={`${f.path} · ${STATUS_LABEL[f.status] ?? f.status}.${also}${guess}`}
                  onClick={() => ws.openView({ kind: 'review', session: a.sessionId, path: f.path, agent: a.name })}
                  onDoubleClick={() => ws.openView({ kind: 'review', session: a.sessionId, path: f.path, agent: a.name }, { pin: true })}
                >
                  <FileIcon name={name} />
                  <span className="wb-row-name truncate">{name}</span>
                  {dir && <span className="wb-result-dir truncate">{dir}</span>}
                  {!!f.also?.length && <Icon name="alert" className="wb-rv-warn" aria-label={`Also changed by ${f.also.join(', ')}`} />}
                  {f.unsure && <span className="wb-rv-guess" aria-hidden="true">?</span>}
                  {f.binary ? (
                    <span className="wb-cv-count text-3">bin</span>
                  ) : (
                    <span className="wb-cv-count tabular">
                      {f.add > 0 && <span className="wb-cv-add">+{f.add}</span>} {f.del > 0 && <span className="wb-cv-del">−{f.del}</span>}
                    </span>
                  )}
                  <span className="wb-row-mark">{STATUS_LETTER[f.status] ?? '•'}</span>
                </button>
              );
            })}
          </section>
        ))}
      </div>
      {undo && (
        <ConfirmDialog
          title={`Undo everything ${undo.name} changed?`}
          confirmLabel="Undo all"
          danger
          onClose={() => setUndo(null)}
          onConfirm={async () => {
            const a = undo;
            try {
              await reviewApi.reject(ws.pid, { session: a.sessionId });
              ws.closeReviewOf(a.sessionId);
              toast.success(`Undid what ${a.name} changed. The text stays in Local History.`);
            } catch (err) {
              toast.error(errorMessage(err));
            } finally {
              void ws.refreshReview();
            }
          }}
        >
          <p>{undo.files.length === 1 ? 'This file goes' : `These ${undo.files.length} files go`} back to what the agent found. The present text stays in Local History.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
