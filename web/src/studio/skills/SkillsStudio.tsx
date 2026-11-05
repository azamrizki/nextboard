// Skills manager (E6, S; docs/API-studio.md §9): Claude Code skills and slash commands of
// a machine — personal, project, plugins (read-only), commands — with their details and a
// local checkup (lint), edit, new, duplicate to the other scope, delete (the hub keeps a
// backup), starters, and Run in a console. Writes into a remote ~/.claude ask first with
// the exact paths (428 protocol, owner decision #3).

import { defaultLoginLabel, defaultProfileOf, useEffectiveSettings } from '../../agents/settings';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { errorMessage, isCode } from '../../api';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { timeAgo } from '../../lib/format';
import type { Project } from '../../lib/types';
import { useMachines, useProfiles, useSessions } from '../../store';
import { skillApi } from '../api';
import { CodeEditor } from '../code/CodeEditor';
import { useRemoteConfirm } from '../confirm';
import { useStudio } from '../store';
import type { LintFinding, Skill, SkillStarter } from '../types';
import '../../styles/studio.css';

type Tab = 'installed' | 'starters';
type Filter = 'all' | 'personal' | 'project' | 'plugin' | 'command';

const SCOPE_LABEL: Record<string, string> = {
  personal: 'Personal',
  project: 'Project',
  personalCommand: 'Personal command',
  projectCommand: 'Project command',
  plugin: 'Plugin',
};

function matches(s: Skill, f: Filter): boolean {
  if (f === 'all') return true;
  if (f === 'command') return s.kind === 'command';
  if (f === 'plugin') return s.scope === 'plugin';
  if (f === 'personal') return s.scope === 'personal';
  return s.scope === 'project';
}

interface Props {
  project: Project;
  onClose: () => void;
}

export default function SkillsStudio({ project, onClose }: Props) {
  const machines = useMachines();
  const [machineId, setMachineId] = useState(project.machineId);
  const machine = machines.find((m) => m.id === machineId);
  const profiles = useProfiles().filter((p) => p.machineId === machineId && p.agent === 'claude');
  const settings = useEffectiveSettings();
  const [profileId, setProfileId] = useState('');
  const projectId = machineId === project.machineId ? project.id : undefined;
  const scopeQ = useMemo(() => ({ projectId, profileId: profileId || undefined }), [projectId, profileId]);
  const [tab, setTab] = useState<Tab>('installed');
  const [filter, setFilter] = useState<Filter>('all');
  const [q, setQ] = useState('');
  const [items, setItems] = useState<Skill[] | null>(null);
  const [cfg, setCfg] = useState('');
  const [error, setError] = useState('');
  const [sel, setSel] = useState<string | null>(null);
  const [creating, setCreating] = useState<Partial<SkillStarter> | null>(null);
  const rev = useStudio((s) => s.skillsRev[machineId] ?? 0);
  const [confirmDialog, run] = useRemoteConfirm();

  const load = useCallback(() => {
    skillApi
      .list(machineId, scopeQ)
      .then((r) => {
        setItems(r.items ?? []);
        setCfg(r.cfg ?? '');
        setError('');
      })
      .catch((err) => setError(errorMessage(err)));
  }, [machineId, scopeQ]);
  useEffect(load, [load, rev]);

  const shown = useMemo(() => {
    const term = q.trim().toLowerCase();
    return (items ?? []).filter((s) => matches(s, filter) && (!term || s.name.toLowerCase().includes(term) || s.description.toLowerCase().includes(term)));
  }, [items, filter, q]);
  const selected = items?.find((s) => s.id === sel) ?? null;

  return (
    <Modal title="Skills" icon="puzzle" size="lg" className="studio-modal" onClose={onClose}>
      <div className="skills-top">
        <SegmentedControl<Tab>
          value={tab}
          onChange={setTab}
          size="sm"
          label="Skills view"
          options={[
            { value: 'installed', label: `Installed${items ? ` · ${items.length}` : ''}` },
            { value: 'starters', label: 'Starters' },
          ]}
        />
        <span className="spacer" />
        {machines.length > 1 && (
          <select className="control control-sm skills-machine" aria-label="Machine" value={machineId} onChange={(e) => setMachineId(e.target.value)}>
            {machines.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </select>
        )}
        {profiles.length > 0 && (
          <select className="control control-sm skills-machine" aria-label="Login profile" value={profileId} onChange={(e) => setProfileId(e.target.value)}>
            <option value="">{defaultLoginLabel(profiles, defaultProfileOf(settings, machineId, 'claude'))}</option>
            {profiles.filter((p) => p.id !== defaultProfileOf(settings, machineId, 'claude')).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        )}
        <Button size="sm" variant="primary" icon="plus" onClick={() => setCreating({})}>
          New skill
        </Button>
      </div>
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {tab === 'starters' ? (
        <Starters onUse={(s) => setCreating(s)} />
      ) : (
        <div className="studio-split skills-split">
          <nav className="studio-side" aria-label="Skills">
            <div className="input-group has-lead">
              <span className="ig-lead">
                <Icon name="search" />
              </span>
              <input className="control control-sm" placeholder="Search skills" aria-label="Search skills" value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <div className="chip-grid" role="radiogroup" aria-label="Filter">
              {(['all', 'personal', 'project', 'plugin', 'command'] as Filter[]).map((f) => (
                <button key={f} type="button" role="radio" aria-checked={filter === f} className={`pick-chip ${filter === f ? 'is-active' : ''}`} onClick={() => setFilter(f)}>
                  {f === 'all' ? 'All' : f === 'plugin' ? 'Plugins' : f === 'command' ? 'Commands' : SCOPE_LABEL[f]}
                </button>
              ))}
            </div>
            <div className="code-side-list nowheel">
              {!items && !error && <p className="list-msg">Reading skills on {machine?.name ?? 'the machine'}…</p>}
              {items && !shown.length && <p className="list-msg">{items.length ? 'Nothing matches.' : 'No skills yet on this machine. Start from a starter or make a new one.'}</p>}
              <ul className="plain studio-list">
                {shown.map((s) => (
                  <li key={s.id}>
                    <button type="button" className={`studio-item ${sel === s.id ? 'is-active' : ''}`} aria-current={sel === s.id || undefined} onClick={() => setSel(s.id)}>
                      <Icon name={s.kind === 'command' ? 'terminal' : 'puzzle'} />
                      <span className="studio-item-text">
                        <span className="truncate">
                          {s.invoke}
                          {s.shadowedBy && <span className="text-3"> · hidden</span>}
                        </span>
                        <span className="fs-xs text-3 truncate">{s.description || 'No description'}</span>
                      </span>
                      {!!s.lint?.length && (
                        <span className="badge badge-warning tabular" title={`${s.lint.length} checkup finding${s.lint.length === 1 ? '' : 's'}`}>
                          {s.lint.length}
                        </span>
                      )}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
            {cfg && <p className="fs-xs text-3 truncate" title={cfg}>Config folder: {cfg}</p>}
          </nav>
          <div className="studio-main">
            {selected ? (
              <SkillPage key={selected.id} skill={selected} machineId={machineId} scopeQ={scopeQ} run={run} onChanged={load} onDeleted={() => {
                setSel(null);
                load();
              }} project={project} />
            ) : (
              <div className="empty-state">
                <span className="empty-icon">
                  <Icon name="puzzle" />
                </span>
                <h3>Pick a skill</h3>
                <p>Skills teach Claude Code a routine it can run with /name. Pick one to see what it does and how it scores in the checkup.</p>
              </div>
            )}
          </div>
        </div>
      )}
      {creating && (
        <NewSkill
          machineName={machine?.name ?? machineId}
          machineId={machineId}
          scopeQ={scopeQ}
          hasProject={!!projectId}
          seed={creating}
          run={run}
          onClose={() => setCreating(null)}
          onCreated={(s) => {
            setCreating(null);
            setTab('installed');
            load();
            setSel(s.id);
          }}
        />
      )}
      {confirmDialog}
    </Modal>
  );
}

type Run = ReturnType<typeof useRemoteConfirm>[1];

function Lint({ lint }: { lint: LintFinding[] | null | undefined }) {
  if (!lint?.length)
    return (
      <p className="lint-ok">
        <Icon name="check" /> Checkup: no findings.
      </p>
    );
  return (
    <ul className="plain lint-list" aria-label="Checkup findings">
      {lint.map((f, i) => (
        <li key={i} className={`lint-item sev-${f.severity}`}>
          <Icon name={f.severity === 'error' ? 'alertCircle' : 'alert'} />
          <span>{f.message}</span>
          <span className="mono fs-xs text-3">{f.rule}</span>
        </li>
      ))}
    </ul>
  );
}

function SkillPage({ skill, machineId, scopeQ, run, onChanged, onDeleted, project }: { skill: Skill; machineId: string; scopeQ: { projectId?: string; profileId?: string }; run: Run; onChanged: () => void; onDeleted: () => void; project: Project }) {
  const [content, setContent] = useState<string | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [rev, setRev] = useState(0);
  const [lint, setLint] = useState(skill.lint);
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [del, setDel] = useState(false);
  const [args, setArgs] = useState('');
  const sessions = useSessions().filter((s) => s.projectId === project.id && s.agentKind === 'agent' && s.status === 'running');
  const [target, setTarget] = useState('');
  const readOnly = skill.scope === 'plugin';

  useEffect(() => {
    skillApi
      .item(machineId, skill.path, scopeQ)
      .then((r) => setContent(r.content))
      .catch((err) => setContent(`Could not read the file: ${errorMessage(err)}`));
  }, [machineId, skill.path, skill.mtime, scopeQ]);

  // live checkup while editing
  useEffect(() => {
    if (edit === null) return;
    const t = window.setTimeout(() => {
      skillApi
        .lint(machineId, edit, skill.path)
        .then((r) => setLint(r.lint))
        .catch(() => undefined);
    }, 500);
    return () => window.clearTimeout(t);
  }, [edit, machineId, skill.path]);

  const save = async () => {
    if (edit === null) return;
    setSaving(true);
    try {
      const r = await run((c) => skillApi.write(machineId, { path: skill.path, content: edit, ifMtime: skill.mtime, ...scopeQ, ...c }));
      if (r) {
        toast.success(`Saved ${skill.invoke}.`);
        setEdit(null);
        setConflict(false);
        onChanged();
      }
    } catch (err) {
      if (isCode(err, 'mtime_mismatch')) setConflict(true);
      else toast.error(`Not saved: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  const duplicate = async () => {
    const to = skill.scope === 'personal' ? 'project' : skill.scope === 'project' ? 'personal' : skill.scope === 'personalCommand' ? 'projectCommand' : 'personalCommand';
    try {
      const r = await run((c) => skillApi.duplicate(machineId, { path: skill.path, toScope: to, ...scopeQ, ...c }));
      if (r) {
        toast.success(`Copied to ${SCOPE_LABEL[to].toLowerCase()}.`);
        onChanged();
      }
    } catch (err) {
      toast.error(isCode(err, 'exists') ? `A ${SCOPE_LABEL[to].toLowerCase()} skill with that name already exists.` : errorMessage(err));
    }
  };

  const runIt = async () => {
    try {
      await skillApi.run(target || sessions[0].id, skill.invoke.replace(/^\//, ''), args.trim() || undefined);
      toast.success(`Sent ${skill.invoke}${args.trim() ? ` ${args.trim()}` : ''} to the console.`);
    } catch (err) {
      toast.error(isCode(err, 'not_agent') ? 'That console isn’t running an agent right now.' : `Not sent: ${errorMessage(err)}`);
    }
  };

  const otherScope = skill.scope === 'personal' || skill.scope === 'personalCommand' ? 'project' : 'personal';
  return (
    <div className="skill-page">
      <div className="skill-head">
        <div className="grow">
          <h3 className="skill-title">
            <span className="mono">{skill.invoke}</span>
            <Button size="sm" variant="ghost" iconOnly icon="copy" aria-label="Copy the command" onClick={() => void navigator.clipboard?.writeText(skill.invoke).then(() => toast.success('Copied.'))} />
          </h3>
          <p className="text-2">{skill.description || 'No description yet — Claude uses it to decide when the skill fits.'}</p>
        </div>
        {!readOnly && edit === null && (
          <div className="row-gap">
            <Button size="sm" icon="pencil" disabled={content === null} onClick={() => setEdit(content ?? '')}>
              Edit
            </Button>
            {skill.scope !== 'plugin' && (
              <Button size="sm" variant="ghost" icon="duplicate" onClick={() => void duplicate()} title={`Copy to ${otherScope}`}>
                Copy to {otherScope}
              </Button>
            )}
            <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label="Delete" onClick={() => setDel(true)} />
          </div>
        )}
      </div>
      <dl className="skill-facts">
        <div>
          <dt>Where</dt>
          <dd>
            {SCOPE_LABEL[skill.scope] ?? skill.scope}
            {skill.plugin ? ` · ${skill.plugin}` : ''}
          </dd>
        </div>
        <div>
          <dt>Called by</dt>
          <dd>{skill.modelInvocable ? 'Claude or you' : 'You only (/name)'}</dd>
        </div>
        <div>
          <dt>Runs in</dt>
          <dd>{skill.subagent ? 'A subagent' : 'The main conversation'}</dd>
        </div>
        {skill.model && (
          <div>
            <dt>Model</dt>
            <dd className="mono">{skill.model}</dd>
          </div>
        )}
        <div>
          <dt>Size</dt>
          <dd className="tabular">≈ {skill.tokensEstimate.toLocaleString()} tokens</dd>
        </div>
        <div>
          <dt>Changed</dt>
          <dd>{timeAgo(skill.mtime)}</dd>
        </div>
      </dl>
      {skill.shadowedBy && (
        <p className="alert warning">
          <Icon name="alert" />
          <span>Hidden by {skill.shadowedBy.replace(/^\w+:/, '')}: a skill with the same name in a stronger place wins (personal beats project, skills beat commands).</span>
        </p>
      )}
      {!!skill.allowedTools?.length && (
        <div className="chips skill-tools" aria-label="Allowed tools">
          {skill.allowedTools.map((t) => (
            <span key={t} className="badge badge-mono">
              {t}
            </span>
          ))}
        </div>
      )}
      {!!skill.siblings?.length && <p className="fs-xs text-3">Files next to it: {skill.siblings.join(', ')}</p>}
      <Lint lint={edit !== null ? lint : skill.lint} />
      {readOnly && <p className="fs-xs text-3">Plugin skills are read-only: they come from an installed plugin.</p>}
      {conflict && (
        <div className="studio-banner is-warning" role="alert">
          <Icon name="alert" />
          <span>The file changed on disk since you opened it. Copy your edits, then reopen the skill.</span>
        </div>
      )}
      {edit !== null ? (
        <>
          <div className="skill-editor">
            <CodeEditor path={skill.path} text={edit} rev={rev} wrap onChange={setEdit} onSave={() => void save()} label={`Editing ${skill.invoke}`} />
          </div>
          <div className="row-end">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setEdit(null);
                setLint(skill.lint);
                setRev((n) => n + 1);
              }}
            >
              Cancel
            </Button>
            <Button size="sm" variant="primary" icon="save" loading={saving} onClick={() => void save()}>
              Save skill
            </Button>
          </div>
        </>
      ) : (
        <pre className="skill-content nowheel">{content ?? 'Loading…'}</pre>
      )}
      <section className="skill-run" aria-label="Run">
        <h3 className="section-title">Run in a console</h3>
        {sessions.length ? (
          <div className="row-gap wrap">
            <select className="control control-sm" aria-label="Console" value={target || sessions[0].id} onChange={(e) => setTarget(e.target.value)}>
              {sessions.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.handle ? `@${s.handle}` : s.name}
                </option>
              ))}
            </select>
            <input className="control control-sm grow mono" placeholder={skill.argumentHint || 'arguments (optional)'} aria-label="Arguments" value={args} maxLength={2000} onChange={(e) => setArgs(e.target.value)} />
            <Button size="sm" variant="primary" icon="play" onClick={() => void runIt()}>
              Run
            </Button>
          </div>
        ) : (
          <p className="fs-xs text-3">Start an agent console in {project.name} to run this skill there.</p>
        )}
      </section>
      {del && (
        <ConfirmDialog
          title={`Delete ${skill.invoke}?`}
          confirmLabel="Delete skill"
          danger
          onClose={() => setDel(false)}
          onConfirm={async () => {
            const r = await run((c) => skillApi.remove(machineId, { path: skill.path, ...scopeQ, ...c }));
            if (r) {
              toast.success(`Deleted ${skill.invoke}. A backup is kept on the hub.`);
              onDeleted();
            }
          }}
        >
          <p>The skill’s files are removed from the machine. NextBoard keeps a copy in the hub’s backups first.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

function Starters({ onUse }: { onUse: (s: SkillStarter) => void }) {
  const [list, setList] = useState<SkillStarter[] | null>(null);
  useEffect(() => {
    skillApi
      .starters()
      .then(setList)
      .catch(() => setList([]));
  }, []);
  if (!list) return <p className="list-msg">Loading starters…</p>;
  return (
    <div className="starter-cards">
      {list.map((s) => (
        <div key={s.name} className="starter-card">
          <div className="starter-card-head">
            <Icon name="puzzle" />
            <span className="mono">/{s.name}</span>
          </div>
          <p className="fs-sm">{s.title}</p>
          <p className="fs-xs text-3">{s.description}</p>
          <div>
            <Button size="sm" icon="plus" onClick={() => onUse(s)}>
              Use this starter
            </Button>
          </div>
        </div>
      ))}
    </div>
  );
}

function NewSkill({
  machineName,
  machineId,
  scopeQ,
  hasProject,
  seed,
  run,
  onClose,
  onCreated,
}: {
  machineName: string;
  machineId: string;
  scopeQ: { projectId?: string; profileId?: string };
  hasProject: boolean;
  seed: Partial<SkillStarter>;
  run: Run;
  onClose: () => void;
  onCreated: (s: Skill) => void;
}) {
  const [name, setName] = useState(seed.name ?? '');
  const [description, setDescription] = useState(seed.description ?? '');
  const [hint, setHint] = useState(seed.argumentHint ?? '');
  const [scope, setScope] = useState<'personal' | 'project'>(hasProject ? 'project' : 'personal');
  const [kind, setKind] = useState<'skill' | 'command'>('skill');
  const [body, setBody] = useState(seed.body ?? '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const nameOk = /^[a-z0-9-]{1,64}$/.test(name);
  const create = async () => {
    setBusy(true);
    setErr('');
    try {
      const s = await run((c) =>
        skillApi.create(machineId, {
          scope: kind === 'command' ? `${scope}Command` : scope,
          kind,
          name,
          description,
          argumentHint: hint || undefined,
          body,
          ...scopeQ,
          ...c,
        }),
      );
      if (s) {
        toast.success(`Created /${name}.`);
        onCreated(s);
      }
    } catch (e) {
      setErr(isCode(e, 'exists') ? 'A skill with that name already exists there.' : errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={seed.name ? `New skill from /${seed.name}` : 'New skill'}
      icon="puzzle"
      size="lg"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" icon="check" loading={busy} disabled={!nameOk || !description.trim() || !body.trim()} onClick={() => void create()}>
            Create skill
          </Button>
        </>
      }
      help={scope === 'personal' ? `Personal: ~/.claude on ${machineName}` : 'Project: .claude in the repository'}
    >
      <div className="form">
        <div className="field-row">
          <div className="field span-6">
            <label className="field-label" htmlFor="sk-name">
              Name
            </label>
            <input id="sk-name" className="control mono" value={name} placeholder="commit-message" aria-invalid={!!name && !nameOk} onChange={(e) => setName(e.target.value)} />
            <span className={name && !nameOk ? 'field-error' : 'field-hint'}>Lowercase letters, digits and hyphens; run it as /{name || 'name'}.</span>
          </div>
          <div className="field span-6">
            <label className="field-label" htmlFor="sk-hint">
              Argument hint <span className="optional">(optional)</span>
            </label>
            <input id="sk-hint" className="control mono" value={hint} placeholder="[scope]" onChange={(e) => setHint(e.target.value)} />
          </div>
        </div>
        <div className="field">
          <label className="field-label" htmlFor="sk-desc">
            Description
          </label>
          <input id="sk-desc" className="control" value={description} maxLength={1024} placeholder="Writes a conventional commit message for the staged changes." onChange={(e) => setDescription(e.target.value)} />
          <span className="field-hint">Claude reads this to decide when the skill fits.</span>
        </div>
        <div className="field-row">
          <div className="field span-6">
            <span className="field-label">Where</span>
            <SegmentedControl<'personal' | 'project'>
              value={scope}
              onChange={setScope}
              size="sm"
              label="Where"
              options={[
                { value: 'project', label: 'This project', disabled: !hasProject },
                { value: 'personal', label: 'Personal' },
              ]}
            />
          </div>
          <div className="field span-6">
            <span className="field-label">Kind</span>
            <SegmentedControl<'skill' | 'command'>
              value={kind}
              onChange={setKind}
              size="sm"
              label="Kind"
              options={[
                { value: 'skill', label: 'Skill' },
                { value: 'command', label: 'Slash command' },
              ]}
            />
          </div>
        </div>
        <div className="field">
          <label className="field-label" htmlFor="sk-body">
            Instructions
          </label>
          <textarea id="sk-body" className="control mono skill-body" value={body} onChange={(e) => setBody(e.target.value)} placeholder="Steps Claude should follow…" />
        </div>
        {err && <p className="field-error">{err}</p>}
      </div>
    </Modal>
  );
}
