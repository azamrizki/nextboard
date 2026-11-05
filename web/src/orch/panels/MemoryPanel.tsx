// Project memory panel (docs/API-orchestration.md §12, D21; M): edit the facts of this
// project or of all projects, scan the repo for proposed facts (nothing is saved until
// you add them), and push the memory to agents as background context.

import { useEffect, useMemo, useState } from 'react';
import { isCode, isStatus } from '../../api';
import type { BoardController } from '../../board/controller';
import { Button } from '../../components/Button';
import { SegmentedControl, SelectField } from '../../components/form';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { timeAgo } from '../../lib/format';
import { Link, memoryPath } from '../../lib/router';
import type { Project } from '../../lib/types';
import { isAgentSession, orchError } from '../actions';
import { orchApi } from '../api';
import { useNow, useWorkspaceSessions } from '../hooks';
import { factsOf, setMemoryCache, useMemoryDoc } from '../memoryDoc';
import { orchStore, useOrch } from '../store';
import { FACT_KINDS, type Fact, type PushResult } from '../types';

type Scope = 'project' | 'global';
type Effort = 'quick' | 'balanced' | 'thorough';

const SKELETON = FACT_KINDS.map((k) => `## ${k}\n\n`).join('');

export default function MemoryPanel({ ctl, project, onClose, onAddCard }: { ctl: BoardController; project: Project; onClose: () => void; onAddCard: () => void }) {
  const mem = useMemoryDoc(project.id);
  const [scope, setScope] = useState<Scope>('project');
  const doc = mem.data?.[scope];
  const [draft, setDraft] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [effort, setEffort] = useState<Effort>('balanced');
  const [agent, setAgent] = useState('claude');
  const [jobId, setJobId] = useState<string | null>(null);
  const job = useOrch((s) => (jobId ? s.jobs[jobId] : undefined));
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [pushing, setPushing] = useState(false);
  const [lastPush, setLastPush] = useState<{ at: string; r: PushResult } | null>(null);
  const sessions = useWorkspaceSessions(ctl.workspaceId).filter((s) => isAgentSession(s) && s.status === 'running');
  const now = useNow(1000, !!job && (job.state === 'running' || job.state === 'queued'));

  useEffect(() => {
    setDraft(null);
    setConflict(false);
  }, [scope]);

  // agents write to this memory on their own: open on the latest
  useEffect(() => {
    mem.reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer') && !(e.target as HTMLElement)?.closest?.('input, textarea, select')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  // job progress: events when the hub sends them, polling as a fallback
  useEffect(() => {
    if (!jobId) return;
    const t = window.setInterval(() => {
      const j = orchStore.get().jobs[jobId];
      if (j && j.state !== 'queued' && j.state !== 'running') return;
      orchApi
        .job(jobId)
        .then((x) => orchStore.upsertJob(x))
        .catch(() => undefined);
    }, 2000);
    return () => window.clearInterval(t);
  }, [jobId]);

  const proposed: Fact[] = useMemo(() => {
    const r = job?.state === 'done' ? (job.result as { facts?: Fact[] } | null) : null;
    return Array.isArray(r?.facts) ? r!.facts : [];
  }, [job]);
  useEffect(() => setPicked(new Set(proposed.map((_, i) => i))), [proposed]);

  const value = draft ?? doc?.content ?? '';
  const dirty = draft !== null && draft !== (doc?.content ?? '');
  const facts = factsOf(doc?.content ?? '');

  const save = async () => {
    if (draft === null) return;
    setSaving(true);
    try {
      const res = await orchApi.saveMemory(project.id, { scope, content: draft, ifMtime: doc?.mtime || undefined });
      setMemoryCache(project.id, scope, res);
      mem.reload();
      setDraft(null);
      toast.success('Memory saved.');
    } catch (err) {
      if (isCode(err, 'mtime_mismatch') || isStatus(err, 409)) setConflict(true);
      else toast.error(`Could not save the memory: ${orchError(err)}`);
    } finally {
      setSaving(false);
    }
  };

  const scan = async () => {
    try {
      const j = await orchApi.scanMemory(project.id, { effort, agent });
      orchStore.upsertJob(j);
      setJobId(j.id);
    } catch (err) {
      toast.error(`Could not start the scan: ${orchError(err)}`);
    }
  };

  const accept = async () => {
    const list = proposed.filter((_, i) => picked.has(i));
    if (!list.length) return;
    try {
      const res = await orchApi.acceptFacts(project.id, { scope, facts: list });
      setMemoryCache(project.id, scope, res);
      mem.reload();
      setJobId(null);
      setDraft(null);
      toast.success(`Added ${list.length} fact${list.length === 1 ? '' : 's'} to the memory.`);
    } catch (err) {
      toast.error(`Could not add the facts: ${orchError(err)}`);
    }
  };

  const pushAll = async () => {
    setPushing(true);
    try {
      const r = await orchApi.pushMemory(project.id, { targets: sessions.map((s) => s.id), scopes: [scope] });
      setLastPush({ at: new Date().toISOString(), r });
      toast.success(`Memory sent to ${r.delivered} agent${r.delivered === 1 ? '' : 's'}${r.busy ? `; ${r.busy} busy get it when free` : ''}.`);
    } catch (err) {
      toast.error(`Could not push the memory: ${orchError(err)}`);
    } finally {
      setPushing(false);
    }
  };

  const running = job && (job.state === 'queued' || job.state === 'running');

  return (
    <aside className="side-panel memory-panel" aria-label="Project memory">
      <div className="side-panel-head">
        <Icon name="brain" />
        <h2>Memory</h2>
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close memory" onClick={onClose} />
      </div>
      <p className="side-panel-hint">
        Facts your agents should keep in mind, one bullet per fact. Sub-memories, critical notes and Init memory are on the <Link to={memoryPath(project.id)}>memory page</Link>.
      </p>
      <SegmentedControl<Scope>
        value={scope}
        onChange={setScope}
        size="sm"
        block
        label="Memory scope"
        options={[
          { value: 'project', label: project.name },
          { value: 'global', label: 'All projects' },
        ]}
      />
      {mem.status === 'unsupported' && <div className="list-msg">This hub has no project memory yet. Update NextBoard on the hub.</div>}
      {mem.status === 'error' && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>Could not load the memory: {mem.error}</span>
        </div>
      )}
      {mem.status !== 'unsupported' && (
        <>
          {scope === 'project' && <p className="setting-hint">One memory for this project. Every agent reads it at launch and can add to it, whichever CLI it uses.</p>}
          <div className="memory-editor">
            <label className="field-label" htmlFor="memory-text">
              {scope === 'project' ? `${project.name} memory` : 'Memory for all projects'} · {facts.length} fact{facts.length === 1 ? '' : 's'}
              {doc?.mtime && <span className="text-3"> · saved {timeAgo(doc.mtime)}</span>}
            </label>
            <textarea id="memory-text" className="control mono memory-textarea" value={value} spellCheck={false} placeholder={SKELETON} onChange={(e) => setDraft(e.target.value)} />
            {conflict && (
              <div className="alert" role="alert">
                <Icon name="alertCircle" />
                <span>The memory changed on another device. Copy your edits, then reload to see the new version.</span>
                <Button
                  size="sm"
                  onClick={() => {
                    setConflict(false);
                    setDraft(null);
                    mem.reload();
                  }}
                >
                  Reload
                </Button>
              </div>
            )}
            <div className="row-end">
              {!value.trim() && (
                <Button size="sm" variant="ghost" onClick={() => setDraft(SKELETON)}>
                  Insert sections
                </Button>
              )}
              {dirty && (
                <Button size="sm" variant="ghost" onClick={() => setDraft(null)} disabled={saving}>
                  Discard
                </Button>
              )}
              <Button size="sm" variant="primary" icon="check" disabled={!dirty} loading={saving} onClick={() => void save()}>
                Save memory
              </Button>
            </div>
          </div>
          {scope === 'project' && (mem.data?.notes?.length ?? 0) > 0 && (
            <details className="memory-notes">
              <summary>Notes saved by agents · {mem.data!.notes!.length}</summary>
              {mem.data!.notes!.map((n) => (
                <div key={n.name} className="memory-note">
                  <div className="mono fs-xs text-3">{n.name}</div>
                  <pre className="mono fs-xs">{n.content}</pre>
                </div>
              ))}
            </details>
          )}

          <section className="memory-section" aria-label="Scan">
            <h3 className="section-title">Scan the project</h3>
            <p className="fs-xs text-3">A hidden read-only run of your CLI reads {project.path} and proposes facts. Nothing is saved until you add them.</p>
            <div className="memory-scan-row">
              <SegmentedControl<Effort>
                value={effort}
                onChange={setEffort}
                size="sm"
                label="Scan effort"
                options={[
                  { value: 'quick', label: 'Quick' },
                  { value: 'balanced', label: 'Balanced' },
                  { value: 'thorough', label: 'Thorough' },
                ]}
              />
              <SelectField label="Run with" value={agent} onChange={(e) => setAgent(e.target.value)} className="control-sm" fieldClassName="memory-agent">
                <option value="claude">Claude Code</option>
                <option value="codex">Codex</option>
              </SelectField>
            </div>
            {running ? (
              <div className="job-row" role="status">
                <span className="spinner" aria-hidden="true" />
                <span>{job?.progress?.text || 'Scanning…'}</span>
                <span className="text-3 tabular">{job?.startedAt ? `${Math.round((now - Date.parse(job.startedAt)) / 1000)}s` : ''}</span>
                <span className="spacer" />
                <Button size="sm" variant="ghost" onClick={() => jobId && void orchApi.cancelJob(jobId).then((j) => orchStore.upsertJob(j)).catch(() => undefined)}>
                  Cancel
                </Button>
              </div>
            ) : (
              <Button size="sm" icon="search" onClick={() => void scan()}>
                Scan project
              </Button>
            )}
            {job?.state === 'failed' && <p className="field-error">The scan failed: {job.error || job.code}</p>}
            {proposed.length > 0 && (
              <div className="fact-list" role="group" aria-label="Proposed facts">
                {proposed.map((f, i) => (
                  <label key={i} className="fact-item">
                    <input
                      type="checkbox"
                      checked={picked.has(i)}
                      onChange={() =>
                        setPicked((cur) => {
                          const n = new Set(cur);
                          if (n.has(i)) n.delete(i);
                          else n.add(i);
                          return n;
                        })
                      }
                    />
                    <span className="cap-chip">{f.kind}</span>
                    <span>{f.text}</span>
                  </label>
                ))}
                <div className="row-end">
                  <Button size="sm" variant="ghost" onClick={() => setJobId(null)}>
                    Discard
                  </Button>
                  <Button size="sm" variant="primary" icon="plus" disabled={!picked.size} onClick={() => void accept()}>
                    Add {picked.size} to memory
                  </Button>
                </div>
              </div>
            )}
          </section>

          <section className="memory-section" aria-label="Push">
            <h3 className="section-title">Brief your agents</h3>
            <p className="fs-xs text-3">The memory is typed into each agent as background context (up to 4000 characters), asking it to reply OK.</p>
            <div className="row-gap wrap">
              <Button size="sm" variant="primary" icon="send" loading={pushing} disabled={!facts.length || !sessions.length} onClick={() => void pushAll()}>
                Push to {sessions.length === 1 ? '1 agent' : `${sessions.length} agents`}
              </Button>
              <Button size="sm" icon="plus" onClick={onAddCard}>
                Add memory card
              </Button>
            </div>
            {lastPush && (
              <p className="fs-xs text-3">
                Pushed {timeAgo(lastPush.at)}: {lastPush.r.delivered} sent{lastPush.r.busy ? `, ${lastPush.r.busy} busy` : ''}
                {lastPush.r.skipped ? `, ${lastPush.r.skipped} skipped` : ''}
                {lastPush.r.notAgent ? `, ${lastPush.r.notAgent} not agents` : ''}.
              </p>
            )}
          </section>
        </>
      )}
    </aside>
  );
}
