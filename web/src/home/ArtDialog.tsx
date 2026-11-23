// "Cover & icon…" of the project menu (DESIGN.md §3.11, API-board §1): per picture Upload,
// Remove, and Create with AI when the hub has an image tool (Codex with image generation). A job
// keeps going when the dialog closes, and shows again when it opens.

import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { api, ApiError, errorMessage, isStatus } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { formatDateTime, prefs, timeAgo } from '../lib/format';
import { packUpload } from '../lib/pack';
import type { ArtCapability, ArtKind, Project } from '../lib/types';
import type { Job } from '../orch/types';
import { store, useProject } from '../store';
import { artJobs, fitImage, jobOver, jobText } from './art';
import { ProjectCover, ProjectIcon } from './ProjectArt';

const NAMES: Record<ArtKind, { title: string; a: string; hint: string; example: string }> = {
  cover: {
    title: 'Cover',
    a: 'a cover',
    hint: 'Across the top of its card. Wide pictures work best (3:2 or wider).',
    example: 'e.g. An online store for game top-ups and vouchers. Show its storefront on a laptop and a phone, dark navy with gold.',
  },
  icon: {
    title: 'Icon',
    a: 'an icon',
    hint: 'Next to its name. A square picture with one bold shape reads best when small.',
    example: 'e.g. A shopping cart carrying a glowing game gem, deep purple with gold.',
  },
};

/** The hub's limit (art.MaxContext). */
export const ART_CONTEXT_MAX = 1000;
const contextKey = (pid: string, kind: ArtKind) => `art.context.${pid}.${kind}`;

/**
 * Before Create with AI: optional words of the owner's for this image, added to what the hub
 * reads from the project's files. The last ones are kept per project and kind for next time.
 */
function ArtContextDialog({ project: p, kind, again, onCreate, onClose }: { project: Project; kind: ArtKind; again: boolean; onCreate: (context: string) => void; onClose: () => void }) {
  const names = NAMES[kind];
  const [text, setText] = useState(() => prefs.get(contextKey(p.id, kind)) ?? '');
  const create = () => {
    const t = text.trim();
    prefs.set(contextKey(p.id, kind), t);
    onCreate(t);
    onClose();
  };
  const keys = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      create();
    }
  };
  return (
    <Modal
      title={`${again ? 'Create again' : 'Create'}: ${names.title.toLowerCase()} of ${p.name}`}
      icon="sparkle"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" icon="sparkle" onClick={create}>
            {text.trim() ? 'Create with this context' : 'Create'}
          </Button>
        </>
      }
    >
      <div className="art-context">
        <label className="field">
          <span className="field-label">
            Context<span className="optional"> (optional)</span>
          </span>
          <textarea
            className="control art-context-text"
            rows={5}
            maxLength={ART_CONTEXT_MAX}
            value={text}
            placeholder={names.example}
            autoFocus
            onChange={(e) => setText(e.target.value)}
            onKeyDown={keys}
          />
          <span className="field-hint art-context-hint">
            <span>What the project is, what it offers, what the {kind} should show, its colours or mood. Empty: it goes by the project’s files alone.</span>
            <span className="tabular">
              {text.length}/{ART_CONTEXT_MAX}
            </span>
          </span>
        </label>
      </div>
    </Modal>
  );
}

const ACCEPT = 'image/png,image/jpeg,image/gif,image/webp';

/** The capability, asked once per page: the hub keeps it a minute too. */
let capability: Promise<ArtCapability> | null = null;
function artCapability(): Promise<ArtCapability> {
  capability ??= api.artCapability().catch(() => ({ available: false, tool: '', reason: 'unknown' }));
  // ask again next time the dialog opens: Codex may have been installed meanwhile
  const asked = capability;
  setTimeout(() => {
    if (capability === asked) capability = null;
  }, 60_000);
  return asked;
}

function ArtSlot({ project: p, kind, ai }: { project: Project; kind: ArtKind; ai: boolean }) {
  const img = p[kind];
  const [job, setJob] = useState<Job | undefined>(() => artJobs.get(p.id, kind));
  const [busy, setBusy] = useState<'' | 'upload' | 'remove' | 'ai' | 'cancel'>('');
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const [asking, setAsking] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const names = NAMES[kind];
  const running = !!job && !jobOver(job);

  /** A job's news: kept while it runs, said once it is over. */
  const settle = (j: Job) => {
    setJob(j);
    if (!jobOver(j)) {
      artJobs.set(p.id, kind, j);
      return;
    }
    artJobs.drop(p.id, kind);
    if (j.state === 'done') {
      setNote('');
      void store.refreshProjects();
    } else if (j.state === 'cancelled') setNote('Cancelled. Nothing was changed.');
    else setError(j.error || 'The image could not be made.');
  };
  const settleRef = useRef(settle);
  settleRef.current = settle;

  // follow the job: its events, and a slow poll in case one is missed (or it ended while closed)
  const jobId = job && !jobOver(job) ? job.id : '';
  useEffect(() => {
    if (!jobId) return;
    const apply = (j: Job) => {
      if (j.id === jobId) settleRef.current(j);
    };
    const off = store.onEvent((ev) => {
      if (ev.type === 'job') apply(ev.job);
    });
    const ask = () => api.job(jobId).then(apply, () => undefined);
    void ask();
    const poll = window.setInterval(ask, 2000);
    return () => {
      off();
      window.clearInterval(poll);
    };
  }, [jobId]);

  const upload = async (file: File) => {
    setBusy('upload');
    setError('');
    setNote('');
    try {
      const fitted = await fitImage(file, kind);
      const packed = await packUpload(fitted, file.name);
      const asset = await api.uploadAssetPacked(packed, fitted.type || file.type);
      store.upsertProject(await api.updateProject(p.id, { [kind]: { assetId: asset.id } }));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };

  const remove = async () => {
    setBusy('remove');
    setError('');
    setNote('');
    try {
      store.upsertProject(await api.updateProject(p.id, { [kind]: null }));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };

  const create = async (context: string) => {
    setBusy('ai');
    setError('');
    setNote('');
    try {
      settle(await api.createArt(p.id, kind, context || undefined));
    } catch (err) {
      const other = err instanceof ApiError && typeof err.body.jobId === 'string' ? err.body.jobId : '';
      // one is being made already (another tab, or before a reload): follow that one
      if (other) settle(await api.job(other).catch(() => ({ id: other, kind: 'art', state: 'running' }) as Job));
      else if (isStatus(err, 409)) setError(`${names.title === 'Cover' ? 'A cover' : 'An icon'} is being made for this project already.`);
      else if (isStatus(err, 412)) setError('No image tool is available on This PC any more.');
      else setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };

  const cancel = async () => {
    if (!job) return;
    setBusy('cancel');
    try {
      settle(await api.cancelJob(job.id));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy('');
    }
  };

  const madeBefore = img?.source === 'ai' || job?.state === 'done';
  const pct = typeof job?.progress?.pct === 'number' ? Math.max(0, Math.min(100, job.progress.pct)) : null;

  return (
    <section className={`art-slot art-slot-${kind}`} aria-labelledby={`art-${kind}-title`}>
      <div className="art-slot-head">
        <h3 id={`art-${kind}-title`}>{names.title}</h3>
        {img && (
          <span className="art-source" title={formatDateTime(img.at)}>
            <Icon name={img.source === 'ai' ? 'sparkle' : 'upload'} />
            {img.source === 'ai' ? 'Made with AI' : 'Uploaded'} · {timeAgo(img.at)}
          </span>
        )}
      </div>
      <div className="art-preview" aria-busy={running || busy === 'upload'}>
        {kind === 'cover' ? <ProjectCover project={p} className="art-cover" /> : <ProjectIcon project={p} className="art-icon" />}
        <p className="art-hint">{names.hint}</p>
      </div>
      {(running || busy === 'upload') && (
        <div className="art-progress" role="status">
          <span className="spinner" aria-hidden="true" />
          <span className="truncate">{busy === 'upload' ? 'Uploading…' : jobText(job!)}</span>
          {running && pct !== null && (
            <span className="art-bar" aria-hidden="true">
              <span style={{ width: `${pct}%` }} />
            </span>
          )}
        </div>
      )}
      <div className="art-actions">
        <input
          ref={fileRef}
          type="file"
          accept={ACCEPT}
          hidden
          aria-label={`Upload ${names.a}`}
          onChange={(e) => {
            const f = e.target.files?.[0];
            e.target.value = '';
            if (f) void upload(f);
          }}
        />
        <Button size="sm" icon="upload" loading={busy === 'upload'} disabled={!!busy || running} onClick={() => fileRef.current?.click()}>
          Upload
        </Button>
        {img && (
          <Button size="sm" variant="ghost" icon="trash" loading={busy === 'remove'} disabled={!!busy || running} onClick={() => void remove()}>
            Remove
          </Button>
        )}
        {ai && !running && (
          <Button size="sm" icon={madeBefore ? 'refresh' : 'sparkle'} loading={busy === 'ai'} disabled={!!busy} onClick={() => setAsking(true)}>
            {madeBefore ? 'Create again' : 'Create with AI'}
          </Button>
        )}
        {running && (
          <Button size="sm" variant="ghost" icon="stop" loading={busy === 'cancel'} disabled={busy === 'cancel'} onClick={() => void cancel()}>
            Cancel
          </Button>
        )}
      </div>
      {asking && <ArtContextDialog project={p} kind={kind} again={madeBefore} onCreate={(c) => void create(c)} onClose={() => setAsking(false)} />}
      {note && <p className="art-hint">{note}</p>}
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
    </section>
  );
}

export function ArtDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const live = useProject(project.id) ?? project;
  const [cap, setCap] = useState<ArtCapability | null>(null);
  useEffect(() => {
    let on = true;
    void artCapability().then((c) => on && setCap(c));
    return () => {
      on = false;
    };
  }, []);
  const ai = !!cap?.available;

  return (
    <Modal
      title={`Cover & icon of ${live.name}`}
      icon="image"
      size="md"
      onClose={onClose}
      help={ai ? `Create with AI sends the project’s name, README excerpt, notes for coding agents, home page text, picture names, descriptions, memory summary and the context you write to ${cap?.tool === 'codex' || !cap?.tool ? 'Codex' : cap.tool}, with your account.` : 'PNG, JPEG, GIF or WebP. Big pictures are made smaller first.'}
      footer={
        <Button variant="secondary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="art-dialog">
        <ArtSlot project={live} kind="cover" ai={ai} />
        <ArtSlot project={live} kind="icon" ai={ai} />
      </div>
    </Modal>
  );
}
