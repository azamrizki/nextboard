// A commit in a tab: its message, who made it and when, its branches and parents, the files it
// changed, and the diff of the chosen file. Merges compare with their first parent.

import { useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { STATUS_LABEL, STATUS_LETTER, STATUS_TONE, splitPath } from '../../git/common';
import { formatDateTime, timeAgo } from '../../lib/format';
import { gitApi } from '../api';
import type { GitCommitDetail, GitCommitFile, GitTarget } from '../types';
import { LANE_COLORS } from './commitGraph';
import { RefPills } from './CommitGraph';
import { FileIcon } from './Explorer';
import { currentTheme, FONT, languageFor, monaco } from './monaco';
import type { CommitTab, Workspace } from './workspace';

/** Narrower than this, the diff starts inline; the button then decides, not the width. */
const SIDE_BY_SIDE_FROM = 900;

export const STASH_LABEL = 'Stash: ';

const copyHash = (hash: string) => void navigator.clipboard?.writeText(hash).then(() => toast.success('Hash copied.'));

function colorOf(name: string): number {
  let n = 0;
  for (const ch of name) n = (n * 31 + ch.charCodeAt(0)) >>> 0;
  return n % LANE_COLORS;
}

function Person({ name, email, at, verb }: { name: string; email: string; at: string; verb: string }) {
  return (
    <span className="wb-cv-person" title={`${name}${email ? ` <${email}>` : ''}\n${formatDateTime(at)}`}>
      <span className="wb-cv-avatar" style={{ background: `var(--wb-lane-${colorOf(name)})` }} aria-hidden="true">
        {name.trim().charAt(0).toUpperCase() || '?'}
      </span>
      <strong className="truncate">{name}</strong>
      {email && <span className="wb-cv-email truncate">{email}</span>}
      <span className="wb-cv-when text-3">
        {verb} {timeAgo(at)}
      </span>
    </span>
  );
}

function FileDiff({ ws, target, detail, file }: { ws: Workspace; target: GitTarget; detail: GitCommitDetail; file: GitCommitFile }) {
  const host = useRef<HTMLDivElement>(null);
  const diffRef = useRef<monaco.editor.IStandaloneDiffEditor | null>(null);
  const models = useRef<monaco.editor.ITextModel[]>([]);
  const [sideBySide, setSideBySide] = useState(true);
  const [note, setNote] = useState('');

  const show = (next: monaco.editor.ITextModel[]) => {
    const diff = diffRef.current;
    diff?.setModel(next.length ? { original: next[0], modified: next[1] } : null);
    models.current.forEach((m) => m.dispose());
    models.current = next;
  };

  useEffect(() => {
    if (!host.current) return;
    const wide = host.current.clientWidth >= SIDE_BY_SIDE_FROM;
    setSideBySide(wide);
    const diff = monaco.editor.createDiffEditor(host.current, {
      theme: currentTheme(),
      automaticLayout: true,
      fontFamily: FONT,
      fontSize: ws.editorPrefs.fontSize,
      renderSideBySide: wide,
      useInlineViewWhenSpaceIsLimited: false,
      originalEditable: false,
      readOnly: true,
      ignoreTrimWhitespace: false,
      hideUnchangedRegions: { enabled: true },
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: false,
    });
    diffRef.current = diff;
    return () => {
      diff.setModel(null);
      diff.dispose();
      diffRef.current = null;
      models.current.forEach((m) => m.dispose());
      models.current = [];
    };
    // the editor lives as long as the view
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  useEffect(() => {
    diffRef.current?.updateOptions({ renderSideBySide: sideBySide });
  }, [sideBySide]);

  useEffect(() => {
    let stop = false;
    setNote('');
    if (file.binary) {
      show([]);
      setNote('Binary file: nothing to compare.');
      return;
    }
    const read = async (path: string, ref: string) => {
      const f = await gitApi.show(target, path, ref);
      if (f.binary) throw new Error('Binary file: nothing to compare.');
      if (f.truncated && !stop) setNote('Too large: only the start is shown.');
      return f.content;
    };
    const language = languageFor(file.path) ?? 'plaintext';
    Promise.all([
      file.status === 'added' || !detail.parents.length ? '' : read(file.oldPath ?? file.path, `${detail.hash}^`),
      file.status === 'deleted' ? '' : read(file.path, detail.hash),
    ])
      .then(([before, after]) => {
        if (!stop) show([monaco.editor.createModel(before, language), monaco.editor.createModel(after, language)]);
      })
      .catch((err) => {
        if (stop) return;
        show([]);
        setNote(errorMessage(err));
      });
    return () => {
      stop = true;
    };
    // show only touches refs
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target, detail.hash, detail.parents.length, file]);

  const here = target.repo === ws.gitTarget.repo && file.status !== 'deleted';
  const base = ws.git?.root?.replace(/\/+$/, '') ?? ws.root;
  return (
    <div className="wb-diff wb-cv-diff">
      <div className="wb-diff-bar">
        <span className="truncate" title={file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}>
          {file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}
        </span>
        {note && <span className="text-3 truncate">{note}</span>}
        <span className="spacer" />
        <Button size="sm" variant="ghost" onClick={() => setSideBySide((v) => !v)}>
          {sideBySide ? 'Inline' : 'Side by side'}
        </Button>
        {here && (
          <Button size="sm" variant="ghost" icon="file" onClick={() => ws.open(`${base}/${file.path}`, { pin: true, source: true })}>
            Open file
          </Button>
        )}
      </div>
      <div className="wb-monaco" ref={host} data-keep-escape="" />
    </div>
  );
}

export function CommitView({ ws, commit }: { ws: Workspace; commit: CommitTab }) {
  const target = useMemo<GitTarget>(() => ({ pid: ws.pid, repo: commit.repo }), [ws.pid, commit.repo]);
  const [detail, setDetail] = useState<GitCommitDetail | null>(null);
  const [error, setError] = useState('');
  const [path, setPath] = useState(commit.file ?? '');

  useEffect(() => {
    let stop = false;
    setDetail(null);
    setError('');
    gitApi
      .commitDetail(target, commit.hash)
      .then((d) => {
        if (stop) return;
        setDetail(d);
        const label = `${d.short} ${d.subject}`;
        if (commit.label !== label && !commit.label.startsWith(STASH_LABEL)) {
          commit.label = label;
          ws.changed();
        }
      })
      .catch((err) => !stop && setError(errorMessage(err)));
    return () => {
      stop = true;
    };
  }, [ws, commit, target]);

  if (error) {
    return (
      <div className="wb-view-empty" role="alert">
        <Icon name="alertCircle" />
        <p>{error}</p>
      </div>
    );
  }
  if (!detail) return <div className="wb-row-msg">Loading the commit…</div>;

  const file = detail.files.find((f) => f.path === path) ?? detail.files[0];
  const add = detail.files.reduce((s, f) => s + f.add, 0);
  const del = detail.files.reduce((s, f) => s + f.del, 0);
  const otherCommitter = detail.committer !== detail.author || detail.committerEmail !== detail.email;
  // git keeps a stash as a merge commit, which says nothing to the one who stashed
  const stash = commit.label.startsWith(STASH_LABEL);

  return (
    <div className="wb-cv nowheel">
      <header className="wb-cv-head">
        <div className="wb-cv-title">
          <h2>{detail.subject}</h2>
          <RefPills refs={detail.refs} color={0} />
        </div>
        {detail.body && <pre className="wb-cv-body">{detail.body}</pre>}
        <div className="wb-cv-meta">
          <Person name={detail.author} email={detail.email} at={detail.at} verb="authored" />
          {otherCommitter && <Person name={detail.committer} email={detail.committerEmail} at={detail.committedAt} verb="committed" />}
          <button type="button" className="wb-cv-hash mono" title={`Copy ${detail.hash}`} onClick={() => copyHash(detail.hash)}>
            <Icon name="copy" />
            {detail.short}
          </button>
          {detail.parents.length > 0 && !stash && (
            <span className="wb-cv-parents">
              <span className="text-3">{detail.parents.length > 1 ? 'Merge of' : 'Parent'}</span>
              {detail.parents.map((p) => (
                <button key={p} type="button" className="wb-cv-hash mono" title={`Open ${p}`} onClick={() => ws.openCommit(p)}>
                  {p.slice(0, detail.short.length)}
                </button>
              ))}
            </span>
          )}
        </div>
        <div className="wb-cv-stats">
          <span>
            {detail.files.length}
            {detail.truncated ? '+' : ''} {detail.files.length === 1 ? 'file' : 'files'} changed
          </span>
          <span className="wb-cv-add tabular">+{add}</span>
          <span className="wb-cv-del tabular">−{del}</span>
          {detail.parents.length > 1 && !stash && <span className="text-3">Against the first parent</span>}
          {detail.truncated && <span className="text-3">Showing the first {detail.files.length} files</span>}
        </div>
      </header>
      {file ? (
        <div className="wb-cv-split">
          <div className="wb-cv-files wb-tree" role="list" aria-label="Changed files">
            {detail.files.map((f) => {
              const { dir, name } = splitPath(f.path);
              return (
                <button
                  key={f.path}
                  type="button"
                  className={`wb-row wb-change wb-cv-file git-${STATUS_TONE[f.status] ?? 'modified'} ${f === file ? 'is-selected' : ''}`}
                  title={`${f.oldPath ? `${f.oldPath} → ` : ''}${f.path} · ${STATUS_LABEL[f.status] ?? f.status}`}
                  aria-current={f === file ? 'true' : undefined}
                  onClick={() => {
                    commit.file = f.path;
                    setPath(f.path);
                  }}
                >
                  <FileIcon name={name} />
                  <span className="wb-row-name truncate">{name}</span>
                  {dir && <span className="wb-result-dir truncate">{dir}</span>}
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
          </div>
          <FileDiff ws={ws} target={target} detail={detail} file={file} />
        </div>
      ) : (
        <div className="wb-row-msg">This commit changes no files.</div>
      )}
    </div>
  );
}
