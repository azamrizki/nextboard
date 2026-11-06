// Documents studio (E2, docs/API-studio.md §5): generate a document about the project
// with a hidden read-only run of the user's CLI (Claude Code / Codex), watch it stream
// in, keep every run as a version, and export it (Markdown, PDF through the print
// route) or add it to the board.

import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../../api';
import { useEffectiveSettings } from '../../agents/settings';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { useStudioPane } from '../StudioPane';
import { Menu } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { timeAgo } from '../../lib/format';
import type { Project } from '../../lib/types';
import { useMachine } from '../../store';
import { orchStore, useOrch } from '../../orch/store';
import { useNow } from '../../orch/hooks';
import { docApi, jobApi } from '../api';
import { MarkdownView } from '../MarkdownView';
import { studioStore, jobActive, useJob, useJobOutput, useStudio } from '../store';
import { DOC_TYPES, type DocStats, type DocType, type DocVersion, type Effort, type StudioDocument } from '../types';
import { DOC_HINT, DOC_LABEL, EFFORT_LABEL, EFFORT_WORDS, PHASE_TEXT, docLabel, download, durationText, fileSafe, printDocUrl, type Paper } from './model';
import '../../styles/studio.css';

interface Props {
  project: Project;
  initialDocument?: string;
  onClose: () => void;
  onAddToBoard: (doc: StudioDocument, versionId: string) => void;
}

type View = 'flow' | 'paged';
const PrintPage = lazy(() => import('../print/PrintPage'));
const AGENTS = [
  { id: 'claude', label: 'Claude Code' },
  { id: 'codex', label: 'Codex' },
] as const;

export default function DocumentsStudio({ project, initialDocument, onClose, onAddToBoard }: Props) {
  const raw = useStudio((s) => s.documents[project.id]);
  // newest activity first
  const docs = useMemo(
    () => (raw ? [...raw].sort((a, b) => (b.versions?.[b.versions.length - 1]?.at ?? '').localeCompare(a.versions?.[a.versions.length - 1]?.at ?? '')) : undefined),
    [raw],
  );
  const [sel, setSel] = useState<string | 'new'>(initialDocument ?? 'new');
  const [loaded, setLoaded] = useState(false);
  const picked = useRef(!!initialDocument);

  useEffect(() => {
    void studioStore.loadDocuments(project.id).finally(() => setLoaded(true));
  }, [project.id]);
  // opening the studio shows the latest document (once)
  useEffect(() => {
    if (!loaded || picked.current) return;
    picked.current = true;
    if (docs?.length) setSel(docs[0].id);
  }, [loaded, docs]);

  const doc = sel === 'new' ? undefined : docs?.find((d) => d.id === sel);
  const pane = useStudioPane('Library');
  return (
    <Modal title="Documents" icon="book" size="lg" className="studio-modal" onClose={onClose}>
      <div className={`studio-split ${pane.splitClass}`}>
        <nav className="studio-side" aria-label="Document library">
          <Button
            size="sm"
            variant={sel === 'new' ? 'primary' : 'secondary'}
            icon="plus"
            block
            onClick={() => {
              picked.current = true;
              setSel('new');
              pane.showMain();
            }}
          >
            New document
          </Button>
          <h3 className="section-title">Library · {project.name}</h3>
          {!loaded && !docs && <p className="list-msg">Loading…</p>}
          {loaded && !docs?.length && <p className="list-msg">No documents yet. Generate one: it is kept here with every version.</p>}
          <ul className="plain studio-list">
            {(docs ?? []).map((d) => {
              const v = d.versions?.[d.versions.length - 1];
              return (
                <li key={d.id}>
                  <button
                    type="button"
                    className={`studio-item ${sel === d.id ? 'is-active' : ''}`}
                    aria-current={sel === d.id || undefined}
                    onClick={() => {
                      picked.current = true;
                      setSel(d.id);
                      pane.showMain();
                    }}
                  >
                    <Icon name="fileText" />
                    <span className="studio-item-text">
                      <span className="truncate">{d.title || docLabel(d.type)}</span>
                      <span className="fs-xs text-3 truncate">
                        {v ? `${v.state === 'running' ? 'Writing now' : v.state === 'incomplete' ? 'Incomplete' : `${v.words} words`} · ${timeAgo(v.at)}` : 'No versions'}
                        {(d.versions?.length ?? 0) > 1 ? ` · ${d.versions!.length} versions` : ''}
                      </span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </nav>
        <div className="studio-main">
          {pane.back}
          {sel === 'new' || !doc ? (
            <Generator project={project} onStarted={(d) => setSel(d.id)} />
          ) : (
            <DocView key={doc.id} project={project} doc={doc} onDeleted={() => setSel('new')} onAddToBoard={onAddToBoard} />
          )}
        </div>
      </div>
    </Modal>
  );
}

function useAgentAvailable(machineId: string) {
  const machine = useMachine(machineId);
  const settings = useEffectiveSettings();
  return (agent: string) => {
    const path = settings.agents.paths?.[machineId]?.[agent];
    if (path) return true;
    const cmds = machine?.probe?.commands;
    return !cmds || !!cmds[agent];
  };
}

function Generator({ project, onStarted, regenerate }: { project: Project; onStarted: (d: StudioDocument) => void; regenerate?: StudioDocument }) {
  const settings = useEffectiveSettings();
  const runnerDefault = ((settings.runner as { defaultAgent?: string } | undefined)?.defaultAgent ?? 'claude') as string;
  const available = useAgentAvailable(project.machineId);
  const [type, setType] = useState<DocType>((regenerate?.type as DocType) ?? 'overview');
  const [ask, setAsk] = useState(regenerate?.ask ?? '');
  const [effort, setEffort] = useState<Effort>('balanced');
  const [agent, setAgent] = useState<string>(available(runnerDefault) ? runnerDefault : available('claude') ? 'claude' : 'codex');
  const [stats, setStats] = useState<DocStats | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let stop = false;
    docApi
      .stats(project.machineId, agent)
      .then((s) => !stop && setStats(s))
      .catch(() => !stop && setStats(null));
    return () => {
      stop = true;
    };
  }, [project.machineId, agent]);

  const go = async () => {
    setBusy(true);
    try {
      const r = await docApi.generate(project.id, { type, ask: type === 'custom' ? ask.trim() : undefined, effort, agent, documentId: regenerate?.id });
      orchStore.upsertJob(r.job);
      studioStore.upsertDocument(project.id, r.document);
      onStarted(r.document);
    } catch (err) {
      toast.error(`Could not start the document: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="doc-gen">
      {!regenerate && (
        <>
          <h3 className="studio-h">What should it cover?</h3>
          <div className="doc-types" role="radiogroup" aria-label="Document type">
            {DOC_TYPES.map((t) => (
              <button key={t} type="button" role="radio" aria-checked={type === t} className={`doc-type ${type === t ? 'is-active' : ''}`} onClick={() => setType(t)}>
                <span className="doc-type-title">{DOC_LABEL[t]}</span>
                <span className="doc-type-hint">{DOC_HINT[t]}</span>
              </button>
            ))}
          </div>
          {type === 'custom' && (
            <div className="field">
              <label className="field-label" htmlFor="doc-ask">
                Your question
              </label>
              <textarea id="doc-ask" className="control doc-ask" value={ask} maxLength={2000} placeholder="How does a request travel from the browser to the database?" onChange={(e) => setAsk(e.target.value)} />
            </div>
          )}
        </>
      )}
      <div className="doc-opts">
        <div className="field">
          <span className="field-label">Effort</span>
          <SegmentedControl<Effort>
            value={effort}
            onChange={setEffort}
            size="sm"
            label="Effort"
            options={(['quick', 'balanced', 'thorough'] as Effort[]).map((e) => ({ value: e, label: `${EFFORT_LABEL[e]} · ${durationText(stats?.[e]?.medianMs)}` }))}
          />
          <span className="field-hint">
            {EFFORT_WORDS[effort]}. Times are the median of the last {stats?.[effort]?.runs || 5} finished runs of this CLI on this machine.
          </span>
        </div>
        <div className="field">
          <span className="field-label">Written by</span>
          <div className="agent-pick" role="radiogroup" aria-label="CLI">
            {AGENTS.map((a) => {
              const ok = available(a.id);
              return (
                <button key={a.id} type="button" role="radio" aria-checked={agent === a.id} className={`agent-opt ${agent === a.id ? 'is-active' : ''} ${ok ? '' : 'is-missing'}`} onClick={() => setAgent(a.id)} title={ok ? '' : `${a.label} wasn’t found on this machine`}>
                  <Icon name="bot" />
                  {a.label}
                  {a.id === runnerDefault && <span className="badge">Default</span>}
                </button>
              );
            })}
          </div>
        </div>
      </div>
      <p className="fs-xs text-3">
        A hidden, read-only run of your CLI in {project.path} (it can read files but not change them or run commands). It uses your own CLI login and plan. Written in
        English.
      </p>
      <div className="row-gap">
        <Button variant="primary" icon="sparkle" loading={busy} disabled={type === 'custom' && !regenerate && !ask.trim()} onClick={() => void go()}>
          {regenerate ? 'Regenerate' : 'Generate'}
        </Button>
      </div>
    </div>
  );
}

function DocView({ project, doc, onDeleted, onAddToBoard }: { project: Project; doc: StudioDocument; onDeleted: () => void; onAddToBoard: (d: StudioDocument, vid: string) => void }) {
  const versions = useMemo(() => [...(doc.versions ?? [])].reverse(), [doc.versions]);
  const [vid, setVid] = useState(versions[0]?.id ?? '');
  const v = versions.find((x) => x.id === vid) ?? versions[0];
  const [text, setText] = useState<string | null>(null);
  const [view, setView] = useState<View>('flow');
  const [paper, setPaper] = useState<Paper>('a4');
  const [confirm, setConfirm] = useState<'' | 'doc' | 'version'>('');
  const [regen, setRegen] = useState(false);
  const job = useJob(v?.state === 'running' ? v.jobId : null);
  const live = useJobOutput(v?.jobId);
  const running = v?.state === 'running' && (jobActive(job) || !job);
  const now = useNow(1000, running);
  const jobs = useOrch((s) => s.jobs);

  // newest version when a new one appears
  const newest = versions[0]?.id;
  useEffect(() => {
    if (newest) setVid(newest);
  }, [newest]);

  useEffect(() => {
    if (!v || v.state === 'running') {
      setText(null);
      return;
    }
    let stop = false;
    setText(null);
    docApi
      .version(doc.id, v.id)
      .then((r) => !stop && setText(r.markdown))
      .catch((err) => !stop && setText(`> Could not load this version: ${errorMessage(err)}`));
    return () => {
      stop = true;
    };
  }, [doc.id, v?.id, v?.state]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!v) return <p className="list-msg">This document has no versions left.</p>;
  if (regen) return <Generator project={project} regenerate={doc} onStarted={() => setRegen(false)} />;

  const body = running ? live : (text ?? '');
  const phase = job?.progress?.phase ?? 'launching';
  const started = job?.startedAt ? Date.parse(job.startedAt) : Date.parse(v.at);
  const agentName = v.agent === 'codex' ? 'Codex' : 'Claude Code';
  const stop = async () => {
    try {
      const j = await jobApi.cancel(v.jobId);
      orchStore.upsertJob(j);
      toast.info('Stopped. What arrived so far is kept as an incomplete version.');
    } catch (err) {
      toast.error(`Could not stop: ${errorMessage(err)}`);
    }
  };
  const del = async () => {
    if (confirm === 'doc' || versions.length === 1) {
      await docApi.remove(doc.id);
      studioStore.setDocuments(project.id, (studioStore.get().documents[project.id] ?? []).filter((d) => d.id !== doc.id));
      onDeleted();
    } else {
      await docApi.removeVersion(doc.id, v.id);
      studioStore.upsertDocument(project.id, { ...doc, versions: (doc.versions ?? []).filter((x) => x.id !== v.id) });
    }
  };
  const failedJob = v.jobId ? jobs[v.jobId] : undefined;

  return (
    <div className="doc-view">
      <div className="doc-bar">
        <div className="doc-title">
          <h3 className="truncate">{doc.title || docLabel(doc.type)}</h3>
          <span className="fs-xs text-3">
            {agentName} · {new Date(v.at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}
            {v.state === 'complete' ? ` · ${v.words} words${v.durationMs ? ` · ${Math.round(v.durationMs / 1000)}s` : ''}` : ''}
          </span>
        </div>
        <span className="spacer" />
        {versions.length > 1 && (
          <select className="control control-sm doc-version" aria-label="Version" value={v.id} onChange={(e) => setVid(e.target.value)}>
            {versions.map((x, i) => (
              <option key={x.id} value={x.id}>
                {i === 0 ? 'Latest' : `v${versions.length - i}`} · {new Date(x.at).toLocaleDateString()} · {x.agent}
                {x.state === 'incomplete' ? ' · incomplete' : x.state === 'running' ? ' · writing' : ''}
              </option>
            ))}
          </select>
        )}
      </div>

      {running ? (
        <div className="job-row" role="status">
          <span className="spinner" aria-hidden="true" />
          <span>
            {PHASE_TEXT[phase] ?? 'Working'} with {agentName}…
          </span>
          <span className="text-3 tabular">{Math.max(0, Math.round((now - started) / 1000))}s</span>
          <span className="spacer" />
          <Button size="sm" variant="ghost" icon="stop" onClick={() => void stop()}>
            Stop
          </Button>
        </div>
      ) : (
        <div className="doc-actions">
          {v.state === 'incomplete' && <span className="badge badge-warning">Incomplete</span>}
          <SegmentedControl<View>
            value={view}
            onChange={setView}
            size="sm"
            label="Preview"
            options={[
              { value: 'flow', label: 'Flowing' },
              { value: 'paged', label: 'Pages' },
            ]}
          />
          <SegmentedControl<Paper>
            value={paper}
            onChange={setPaper}
            size="sm"
            label="Paper"
            options={[
              { value: 'a4', label: 'A4' },
              { value: 'letter', label: 'Letter' },
            ]}
          />
          <span className="spacer" />
          <Button size="sm" icon="printer" disabled={!text} onClick={() => window.open(printDocUrl(doc.id, v.id, paper), '_blank', 'noopener')}>
            {v.state === 'incomplete' ? 'Export draft PDF' : 'PDF'}
          </Button>
          <Button size="sm" variant="ghost" icon="plus" onClick={() => onAddToBoard(doc, v.id)}>
            Add to board
          </Button>
          <Menu
            label="More document actions"
            items={[
              { label: 'Export Markdown', icon: 'download', disabled: !text, onSelect: () => text && download(`${fileSafe(doc.title || doc.type)}.md`, text) },
              { label: 'Copy Markdown', icon: 'copy', disabled: !text, onSelect: () => text && void navigator.clipboard?.writeText(text).then(() => toast.success('Markdown copied.')) },
              { label: 'Regenerate…', icon: 'refresh', onSelect: () => setRegen(true) },
              'sep',
              { label: versions.length > 1 ? 'Delete this version…' : 'Delete document…', icon: 'trash', danger: true, onSelect: () => setConfirm(versions.length > 1 ? 'version' : 'doc') },
            ]}
          />
        </div>
      )}
      {failedJob?.state === 'failed' && v.state !== 'complete' && <p className="field-error">The run failed: {failedJob.error || failedJob.code}</p>}

      <div className="doc-preview nowheel" aria-busy={running || undefined}>
        {view === 'paged' && !running && text ? (
          <Suspense fallback={<p className="list-msg">Laying out pages…</p>}>
            <PrintPage kind="documents" docId={doc.id} versionId={v.id} inline paper={paper} />
          </Suspense>
        ) : body ? (
          <MarkdownView text={body} className="doc-md" />
        ) : (
          <p className="list-msg">{running ? 'The page appears here as it is written…' : 'Loading…'}</p>
        )}
      </div>

      {confirm && (
        <ConfirmDialog
          title={confirm === 'doc' ? 'Delete this document?' : 'Delete this version?'}
          confirmLabel={confirm === 'doc' ? 'Delete document' : 'Delete version'}
          danger
          onClose={() => setConfirm('')}
          onConfirm={del}
        >
          <p>{confirm === 'doc' ? 'Every version is removed from the hub for good.' : 'This version is removed from the hub for good; the others stay.'}</p>
          {confirm === 'version' && (
            <p>
              <button type="button" className="link-btn" onClick={() => setConfirm('doc')}>
                Delete the whole document instead
              </button>
            </p>
          )}
        </ConfirmDialog>
      )}
    </div>
  );
}

export type { DocVersion };
