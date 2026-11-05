// One memory document, to read and to edit, like a note in a vault. [[Links]] open
// what they point at; critical notes say what makes them different.

import { useEffect, useMemo, useState, type MouseEvent } from 'react';
import { errorMessage, isCode, isStatus } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon, type IconName } from '../components/Icon';
import { TextField } from '../components/form';
import { toast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import { renderMarkdown } from '../lib/markdown';
import { memoryApi } from './api';
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

export function DocPanel({
  doc,
  owner,
  ownerName,
  rev,
  startEditing,
  onClose,
  onOpen,
  onChanged,
}: {
  doc: DocRef;
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
      if (r.delivered + r.busy === 0) toast.info('No agent is running on this project right now. They get the rules at launch.');
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
