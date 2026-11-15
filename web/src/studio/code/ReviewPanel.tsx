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
import type { PartService } from './partsvc';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

/** What agents changed, by agent; a Root card has one section per part (each part keeps its own review). */
export function ReviewPanel({ ws }: { ws: Workspace }) {
  useWs(ws);
  const [undo, setUndo] = useState<{ part: PartService; agent: ReviewAgent } | null>(null);
  const [busy, setBusy] = useState('');
  const refreshAll = () => Promise.all(ws.parts.map((p) => p.refreshReview()));

  useEffect(() => {
    void Promise.all(ws.parts.map((p) => p.refreshReview()));
  }, [ws]);

  const acceptAll = async (part: PartService, a: ReviewAgent) => {
    setBusy(a.sessionId);
    try {
      await reviewApi.accept(part.ref, { session: a.sessionId });
      closeReviews(ws, part, a.sessionId);
      toast.success(`Kept what ${a.name} changed.`);
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy('');
      void part.refreshReview();
    }
  };

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2 className="truncate">Review</h2>
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh" title="Refresh" onClick={() => void refreshAll()} />
      </header>
      <div className="wb-tree nowheel">
        {ws.parts.map((part) => (
          <PartReview key={part.partId} ws={ws} part={part} titled={ws.paths.multi} busy={busy} onUndo={(a) => setUndo({ part, agent: a })} onKeep={(a) => void acceptAll(part, a)} />
        ))}
      </div>
      {undo && (
        <ConfirmDialog
          title={`Undo everything ${undo.agent.name} changed${ws.paths.multi ? ` in ${undo.part.name}` : ''}?`}
          confirmLabel="Undo all"
          danger
          onClose={() => setUndo(null)}
          onConfirm={async () => {
            const { part, agent: a } = undo;
            try {
              await reviewApi.reject(part.ref, { session: a.sessionId });
              closeReviews(ws, part, a.sessionId);
              toast.success(`Undid what ${a.name} changed. The text stays in Local History.`);
            } catch (err) {
              toast.error(errorMessage(err));
            } finally {
              void part.refreshReview();
            }
          }}
        >
          <p>{undo.agent.files.length === 1 ? 'This file goes' : `These ${undo.agent.files.length} files go`} back to what the agent found. The present text stays in Local History.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

/** The review tabs of one agent in one part close; a root console's tabs in other parts stay. */
function closeReviews(ws: Workspace, part: PartService, session: string) {
  const ids = ws.groups.flatMap((g) => g.tabs.filter((t) => t.view?.kind === 'review' && t.view.session === session && t.view.part === part.partId).map((t) => t.id));
  for (const id of ids) ws.closeSpecial(id);
}

/** One part's review: its agents and what each changed (titled: under the part's name, in a Root card). */
function PartReview({ ws, part, titled, busy, onUndo, onKeep }: { ws: Workspace; part: PartService; titled: boolean; busy: string; onUndo: (a: ReviewAgent) => void; onKeep: (a: ReviewAgent) => void }) {
  const s = part.review;
  const count = s?.agents.reduce((n, a) => n + a.files.length, 0) ?? 0;
  return (
    <>
      {titled && (
        <div className="wb-section-title wb-part-head" title={`${part.name}: ${part.root}`}>
          <Icon name="folder" className="wb-icon" />
          <span className="truncate">{part.name}</span>
          <span className="spacer" />
          <span className="wb-result-count tabular">{count}</span>
        </div>
      )}
      {!s && <div className="wb-row-msg">Loading…</div>}
      {s && !s.supported && <div className="wb-row-msg">A project on a whole home folder takes no part in review: it would copy every login and other project.</div>}
      {s?.problem && (
        <div className="wb-dbg-notice" role="alert">
          <p>{s.problem}</p>
        </div>
      )}
      {s?.supported && !s.agents.length && <div className="wb-row-msg">{titled ? 'Nothing to review here.' : 'Nothing to review. What agents change shows up here, by agent.'}</div>}
      {s?.agents.map((a) => (
        <section key={a.sessionId} className="wb-rv-agent" aria-label={`Changes by ${a.name}`}>
          <div className="wb-section-title wb-rv-head">
            {a.working && <span className="dot dot-success" title={`${a.name} is at work`} aria-hidden="true" />}
            <span className="truncate" title={`${a.name} · ${a.kind}`}>
              {a.name}
            </span>
            <span className="wb-result-count tabular">{a.files.length}</span>
            <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => onUndo(a)}>
              Undo all
            </Button>
            <Button size="sm" variant="ghost" disabled={!!busy} loading={busy === a.sessionId} onClick={() => onKeep(a)}>
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
                className={`wb-row wb-change wb-rv-file git-${STATUS_TONE[f.status] ?? 'modified'} ${ws.activeTab?.view?.kind === 'review' && ws.activeTab.view.part === part.partId && ws.activeTab.view.session === a.sessionId && ws.activeTab.view.path === f.path ? 'is-selected' : ''}`}
                title={`${f.path} · ${STATUS_LABEL[f.status] ?? f.status}.${also}${guess}`}
                onClick={() => ws.openView({ kind: 'review', part: part.partId, session: a.sessionId, path: f.path, agent: a.name })}
                onDoubleClick={() => ws.openView({ kind: 'review', part: part.partId, session: a.sessionId, path: f.path, agent: a.name }, { pin: true })}
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
    </>
  );
}
