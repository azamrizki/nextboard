// One memory document, to read and to edit, like a note in a vault. [[Links]] open
// what they point at; critical notes say what makes them different.

import { useEffect, useMemo, useState, type MouseEvent } from 'react';
import { api, errorMessage, isCode, isStatus } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon, type IconName } from '../components/Icon';
import { TextField } from '../components/form';
import { toast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import { renderMarkdown } from '../lib/markdown';
import { memoryApi } from './api';
import { commandLines } from './blocks';
import { SendMemoryDialog } from './SendMemoryDialog';
import { useProject } from '../store';
import type { SandboxStatus } from '../lib/types';
import type { DocRef, GalaxyOwner, MemoryDocument } from './types';

const LINK_PREFIX = '#memory-';

/** [[name]], [[name|shown]] and [[name#part]] become links the panel catches. */
function linked(content: string) {
  return content.replace(/\[\[([^\]\n]+)\]\]/g, (_, inner: string) => {
    const [target, shown] = inner.split('|');
    const name = target.split('#')[0].trim();
    return `[${(shown ?? target).trim()}](${LINK_PREFIX}${encodeURIComponent(name)})`;
  });
}

function badgeOf(d: MemoryDocument): { icon: IconName; label: string } {
  if (d.critical) return { icon: 'shieldAlert', label: 'Critical notes' };
  if (d.kind === 'main') return { icon: 'brain', label: 'Main memory' };
  if (d.kind === 'note') return { icon: 'note', label: 'Agent note' };
  return { icon: 'layers', label: 'Sub-memory' };
}

/**
 * Commands the CLI itself stops at, whatever the model decides. Critical notes are what an agent
 * is told; this is what it cannot simply talk itself past.
 */
function BlockedCommands({ owner, ownerName }: { owner: string; ownerName: string }) {
  const [saved, setSaved] = useState<string | null>(null);
  const [text, setText] = useState('');
  const [inherited, setInherited] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    setSaved(null);
    memoryApi
      .blocked(owner)
      .then((r) => {
        if (!alive) return;
        const value = r.commands.join('\n');
        setSaved(value);
        setText(value);
        setInherited(r.inherited ?? []);
      })
      .catch((err) => alive && setError(errorMessage(err)));
    return () => {
      alive = false;
    };
  }, [owner]);

  const save = async () => {
    setBusy(true);
    setError('');
    try {
      const r = await memoryApi.saveBlocked(owner, commandLines(text));
      const value = r.commands.join('\n');
      setSaved(value);
      setText(value);
      toast.success('Saved. It counts from the next time a console starts.');
    } catch (err) {
      setError(isStatus(err, 400) ? 'A line is too short or too long, or the list is over 64 commands.' : errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  if (saved === null && !error) return null;
  return (
    <section className="mem-blocked" aria-label="Blocked commands">
      <h3 className="section-title">Blocked commands</h3>
      <p className="text-3">
        The CLI stops and asks you in the console before running these, whatever the model decides. One per line: the start of a command, or * for anything, as in *db:reset*. Not a sandbox: an agent can still write a script that does the same.
      </p>
      <textarea className="control mono" aria-label={`Blocked commands of ${ownerName}`} value={text} spellCheck={false} onChange={(e) => setText(e.target.value)} />
      {inherited.length > 0 && <p className="text-3">Also from all projects: {inherited.join(', ')}</p>}
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      <div className="row-end">
        <Button size="sm" icon="check" loading={busy} disabled={text === saved} onClick={() => void save()}>
          Save commands
        </Button>
      </div>
    </section>
  );
}

/** A project's sandbox for its agents' commands (docs/API-agents.md §27). */
function ProjectSandbox({ projectId }: { projectId: string }) {
  const project = useProject(projectId);
  const [status, setStatus] = useState<SandboxStatus | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.sandboxStatus().then(setStatus, () => setStatus(null));
  }, []);
  if (!project) return null;
  const value = project.sandbox ?? '';
  const change = async (v: string) => {
    setBusy(true);
    try {
      await api.updateProject(projectId, { sandbox: v });
      toast.success('Saved. It counts from the next time a console starts.');
    } catch (err) {
      toast.error(`Could not save: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="mem-sandbox" aria-label="Agent sandbox">
      <h3 className="section-title">Sandbox</h3>
      <p className="text-3">
        A lock, not an instruction: the commands Claude Code and Codex run may write only in this project, the temp folder and package caches. OpenCode and other CLIs have no sandbox, so their commands are not limited.
      </p>
      <select className="control control-sm" aria-label={`Sandbox of ${project.name}`} value={value} disabled={busy} onChange={(e) => void change(e.target.value)}>
        <option value="">Off: what each CLI does by itself</option>
        <option value="workspace">Write only in this project</option>
        <option value="offline">Write only in this project, no network</option>
      </select>
      {value && status && !status.claude.ready && (
        <p className="field-error" role="alert">
          Claude Code cannot build its sandbox on This PC: {status.claude.reason}. Its commands fail until that is fixed, so none runs outside it. The fix is the AppArmor step in{' '}
          <a href="https://code.claude.com/docs/en/sandboxing" target="_blank" rel="noreferrer">
            Claude Code's sandboxing docs
          </a>
          . Codex is not affected.
        </p>
      )}
    </section>
  );
}

export function DocPanel({
  doc,
  owner,
  ownerName,
  rev,
  startEditing,
  onClose,
  onOpen,
  onChanged,
  projects,
}: {
  doc: DocRef;
  /** Where a memory of all projects can be sent to. */
  projects: { id: string; name: string }[];
  owner: GalaxyOwner | undefined;
  ownerName: string;
  rev: number;
  startEditing: boolean;
  onClose: () => void;
  onOpen: (ref: DocRef) => void;
  onChanged: () => void;
}) {
  const [data, setData] = useState<MemoryDocument | null>(null);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState(startEditing);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [content, setContent] = useState('');
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [pushing, setPushing] = useState(false);
  const [sending, setSending] = useState(false);

  useEffect(() => {
    setEditing(startEditing);
    setConflict(false);
    setError('');
    setData(null);
  }, [doc.owner, doc.name, startEditing]);

  // Agents write too: show the latest, but never pull text out from under an edit.
  useEffect(() => {
    if (editing && data) return;
    let alive = true;
    memoryApi
      .doc(doc.owner, doc.name)
      .then((d) => {
        if (!alive) return;
        setData(d);
        setTitle(d.title);
        setDescription(d.description ?? '');
        setContent(d.content);
      })
      .catch((err) => alive && setError(errorMessage(err)));
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc.owner, doc.name, rev]);

  const html = useMemo(() => (data?.content ? renderMarkdown(linked(data.content), undefined, { anchors: true }) : ''), [data?.content]);

  const onBodyClick = (e: MouseEvent) => {
    const a = (e.target as HTMLElement).closest('a');
    const href = a?.getAttribute('href') ?? '';
    if (!href.startsWith(LINK_PREFIX)) return;
    e.preventDefault();
    const wanted = decodeURIComponent(href.slice(LINK_PREFIX.length)).toLowerCase();
    const to = owner?.docs.find((d) => d.name.toLowerCase() === wanted || d.title.toLowerCase() === wanted || d.name.toLowerCase() === `note:${wanted}.md`);
    if (to) onOpen({ owner: doc.owner, name: to.name });
    else toast.error(`Nothing named ${wanted} in this memory yet.`);
  };

  const save = async () => {
    if (!data) return;
    setSaving(true);
    setError('');
    try {
      const d = await memoryApi.save(doc.owner, doc.name, { title, description, content, ifMtime: data.mtime || undefined });
      setData(d);
      setEditing(false);
      onChanged();
      toast.success(`${d.title} saved.`);
    } catch (err) {
      if (isCode(err, 'mtime_mismatch') || isStatus(err, 409)) setConflict(true);
      else setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const pushRules = async () => {
    setPushing(true);
    try {
      const r = await memoryApi.pushCritical(doc.owner);
      if (r.delivered + r.busy === 0 && r.upToDate) toast.info('Every running agent has these rules already.');
      else if (r.delivered + r.busy === 0) toast.info('No agent is running on this project right now. They get the rules at launch.');
      else toast.success(`Rules sent to ${r.delivered + r.busy} running agent${r.delivered + r.busy === 1 ? '' : 's'}.`);
    } catch (err) {
      toast.error(`Could not send the rules: ${errorMessage(err)}`);
    } finally {
      setPushing(false);
    }
  };

  const meta = owner?.docs.find((d) => d.name === doc.name);
  const backlinks = owner?.docs.filter((d) => d.links.includes(doc.name)) ?? [];
  const badge = data ? badgeOf(data) : null;
  const readOnly = data?.kind === 'note';
  const dirty = !!data && (content !== data.content || title !== data.title || description !== (data.description ?? ''));

  return (
    <aside className={`mem-panel mem-doc ${data?.critical ? 'is-critical' : ''}`} aria-label={data?.title ?? 'Memory document'}>
      <header className="mem-doc-head">
        {badge && (
          <span className={`badge ${data?.critical ? 'badge-danger' : ''}`}>
            <Icon name={badge.icon} />
            {badge.label}
          </span>
        )}
        <span className="spacer" />
        {data && !data.critical && !editing && (
          <Button size="sm" icon="send" onClick={() => setSending(true)}>
            Send
          </Button>
        )}
        {data && !readOnly && !editing && (
          <Button size="sm" icon="pencil" onClick={() => setEditing(true)}>
            Edit
          </Button>
        )}
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close" onClick={onClose} />
      </header>

      <div className="mem-doc-title">
        <h2>{data?.title ?? ' '}</h2>
        <p className="fs-xs text-3">
          {ownerName}
          {meta ? ` · ${meta.facts ? `${meta.facts} ${data?.critical ? 'rule' : 'fact'}${meta.facts === 1 ? '' : 's'}` : `${meta.chars} characters`}` : ''}
          {data?.mtime ? ` · saved ${timeAgo(data.mtime)}` : ''}
        </p>
      </div>

      {sending && (
        <SendMemoryDialog
          projectId={doc.owner || projects[0]?.id || ''}
          projects={doc.owner ? undefined : projects}
          initial={[doc.owner ? { projectId: doc.owner, name: doc.name } : { name: doc.name }]}
          onClose={() => setSending(false)}
        />
      )}

      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}

      {data?.critical && (
        <div className="mem-critical-note">
          <Icon name="shieldAlert" />
          <div>
            <p>Every agent gets these first, whole, at each launch and in every memory it reads. Agents cannot change them; only you can.</p>
            <p className="text-3">They are firm instructions, not a technical lock: an agent still has to obey them. Agents that are already running get changes when you send them.</p>
            {doc.owner && (
              <Button size="sm" icon="send" onClick={() => void pushRules()} loading={pushing} className="mt-2">
                Send to running agents
              </Button>
            )}
            <BlockedCommands owner={doc.owner} ownerName={ownerName} />
            {doc.owner && <ProjectSandbox projectId={doc.owner} />}
          </div>
        </div>
      )}

      {editing ? (
        <div className="mem-doc-edit">
          {data?.kind === 'sub' && !data.critical && (
            <>
              <TextField label="Title" value={title} maxLength={80} onChange={(e) => setTitle(e.target.value)} />
              <TextField label="When to read it" value={description} maxLength={300} onChange={(e) => setDescription(e.target.value)} hint="Agents choose what to look up by this." />
            </>
          )}
          <textarea
            className="control mono mem-doc-text"
            value={content}
            spellCheck={false}
            aria-label={`${data?.title ?? 'Memory'} text`}
            placeholder={data?.critical ? '- Never run `make wipe`: it empties the production database.' : '## A heading\n\n- One fact per bullet. Link other pages with [[their-name]].'}
            onChange={(e) => setContent(e.target.value)}
            autoFocus={startEditing}
          />
          {conflict && (
            <div className="alert" role="alert">
              <Icon name="alertCircle" />
              <span>This changed elsewhere while you were writing. Copy your text, then reload.</span>
              <Button
                size="sm"
                onClick={() => {
                  setConflict(false);
                  setEditing(false);
                  onChanged();
                  void memoryApi.doc(doc.owner, doc.name).then((d) => {
                    setData(d);
                    setTitle(d.title);
                    setDescription(d.description ?? '');
                    setContent(d.content);
                  });
                }}
              >
                Reload
              </Button>
            </div>
          )}
          <div className="mem-doc-actions">
            {data?.kind === 'sub' && !data.critical && (
              <Button size="sm" variant="danger-ghost" icon="trash" onClick={() => setDeleting(true)} disabled={saving} className="mem-doc-delete">
                Delete
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              disabled={saving}
              onClick={() => {
                setEditing(false);
                if (data) {
                  setTitle(data.title);
                  setDescription(data.description ?? '');
                  setContent(data.content);
                }
              }}
            >
              Cancel
            </Button>
            <Button size="sm" variant="primary" icon="check" disabled={!dirty} loading={saving} onClick={() => void save()}>
              Save
            </Button>
          </div>
        </div>
      ) : (
        <div className="mem-doc-body">
          {data?.description && <p className="mem-doc-desc">{data.description}</p>}
          {html ? (
            // eslint-disable-next-line react/no-danger -- sanitised by renderMarkdown
            <div className="md-body mem-md" onClick={onBodyClick} dangerouslySetInnerHTML={{ __html: html }} />
          ) : (
            data && <p className="text-3">Nothing here yet.{readOnly ? '' : ' Press Edit to start.'}</p>
          )}
          {(meta?.links.length || backlinks.length) && owner ? (
            <div className="mem-links">
              {meta && meta.links.length > 0 && (
                <div>
                  <h3 className="section-title">Links to</h3>
                  <div className="chips">
                    {meta.links.map((name) => (
                      <button key={name} type="button" className="mem-chip" onClick={() => onOpen({ owner: doc.owner, name })}>
                        {owner.docs.find((d) => d.name === name)?.title ?? name}
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {backlinks.length > 0 && (
                <div>
                  <h3 className="section-title">Linked from</h3>
                  <div className="chips">
                    {backlinks.map((d) => (
                      <button key={d.name} type="button" className="mem-chip" onClick={() => onOpen({ owner: doc.owner, name: d.name })}>
                        {d.title}
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          ) : null}
        </div>
      )}

      {deleting && data && (
        <ConfirmDialog
          title={`Delete ${data.title}?`}
          confirmLabel="Delete sub-memory"
          danger
          onClose={() => setDeleting(false)}
          onConfirm={async () => {
            await memoryApi.remove(doc.owner, doc.name);
            onChanged();
            onClose();
          }}
        >
          <p>This page is removed from the memory of {ownerName}, and agents can no longer look it up. It cannot be undone.</p>
        </ConfirmDialog>
      )}
    </aside>
  );
}
