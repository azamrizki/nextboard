// Settings › NextCode: what NextCode brings to every conversation, whichever account runs it
// (docs/API-agents.md §30, Clean NextCode). Nothing of a CLI's own setup or an account's goes in
// (its settings, connectors, skills, plugins, instructions files); NextCode has its own:
//   - Skills: SKILL.md files the system prompt lists, which the agent reads when a task matches;
//   - Memory: notes kept across conversations, global and per project, which it keeps itself;
//   - MCP servers: those of the hub's (Settings › MCP servers) that NextCode gets.

import { useCallback, useEffect, useState } from 'react';
import { api, errorMessage } from '../api';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { Checkbox, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { Link } from '../lib/router';
import type { McpServer } from '../lib/types';
import { kitApi, type NcMemoryScope, type NcSkill } from '../nextcode/api';
import { useProjects } from '../store';

/** What a skill's name may be (the hub's rule). */
const SKILL_NAME = /^[a-z0-9][a-z0-9-]{0,63}$/;

export function SkillsCard() {
  const [skills, setSkills] = useState<NcSkill[] | null>(null);
  const [editing, setEditing] = useState<NcSkill | 'new' | null>(null);
  const load = useCallback(() => {
    kitApi
      .skills()
      .then((r) => setSkills(r.items))
      .catch((err) => toast.error(`Could not read NextCode's skills: ${errorMessage(err)}`));
  }, []);
  useEffect(load, [load]);
  const remove = async (s: NcSkill) => {
    try {
      await kitApi.deleteSkill(s.name);
      load();
    } catch (err) {
      toast.error(`Could not remove ${s.name}: ${errorMessage(err)}`);
    }
  };
  return (
    <section className="card settings-card" aria-labelledby="nc-skills">
      <div className="settings-card-head">
        <h3 id="nc-skills" className="section-title">
          Skills
        </h3>
        <Button size="sm" variant="primary" icon="plus" onClick={() => setEditing('new')}>
          Add skill
        </Button>
      </div>
      <p className="setting-hint">
        NextCode’s own skills, on every account: how to do a kind of task (a release check, the house style). Every conversation is told their names and when to use them, and reads one
        when a task matches it. The skills of a login or a CLI never come along.
      </p>
      {skills === null ? (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading…
        </div>
      ) : skills.length === 0 ? (
        <div className="list-msg">No skill yet.</div>
      ) : (
        <ul className="nc-accounts" aria-label="NextCode’s skills">
          {skills.map((s) => (
            <li key={s.name} className="nc-account">
              <Icon name="sparkle" />
              <span className="nc-account-text">
                <span className="nc-account-name mono">{s.name}</span>
                <span className="nc-account-detail">{s.description}</span>
              </span>
              <span className="nc-account-actions">
                <Button size="sm" variant="secondary" onClick={() => setEditing(s)}>
                  Edit
                </Button>
                <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label={`Remove the skill ${s.name}`} onClick={() => void remove(s)} />
              </span>
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <SkillDialog
          skill={editing === 'new' ? null : editing}
          taken={(skills ?? []).map((s) => s.name)}
          onClose={(saved) => {
            setEditing(null);
            if (saved) load();
          }}
        />
      )}
    </section>
  );
}

function SkillDialog({ skill, taken, onClose }: { skill: NcSkill | null; taken: string[]; onClose: (saved: boolean) => void }) {
  const [name, setName] = useState(skill?.name ?? '');
  const [description, setDescription] = useState(skill?.description ?? '');
  const [body, setBody] = useState('');
  const [loaded, setLoaded] = useState(!skill);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!skill) return;
    kitApi
      .skill(skill.name)
      .then((s) => {
        setBody(s.body ?? '');
        setLoaded(true);
      })
      .catch((err) => setError(errorMessage(err)));
  }, [skill]);
  const nameProblem = !skill && name && (!SKILL_NAME.test(name) ? 'Lower case letters, digits and hyphens.' : taken.includes(name) ? 'There is a skill by that name.' : '');
  const save = async () => {
    setBusy(true);
    setError('');
    try {
      await kitApi.putSkill(name, description, body);
      onClose(true);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title={skill ? `Skill · ${skill.name}` : 'Add a skill'}
      icon="sparkle"
      size="lg"
      onClose={() => onClose(false)}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={() => onClose(false)} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!loaded || !name || !!nameProblem || !description.trim() || !body.trim()} onClick={() => void save()}>
            Save
          </Button>
        </>
      }
    >
      <div className="stack">
        <TextField label="Name" value={name} onChange={(e) => setName(e.target.value.trim())} disabled={!!skill} mono placeholder="release-check" error={nameProblem || undefined} maxLength={64} />
        <TextField label="When to use it" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Before a release: what to check, in order" maxLength={300} />
        <label className="field">
          <span className="field-label">What to do (Markdown)</span>
          <textarea className="control mono nc-kit-text" rows={14} value={body} onChange={(e) => setBody(e.target.value)} disabled={!loaded} placeholder={'1. Run make test-fast\n2. …'} />
        </label>
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </Modal>
  );
}

export function MemoryCard() {
  const projects = useProjects();
  const [scopes, setScopes] = useState<NcMemoryScope[] | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const load = useCallback(() => {
    kitApi
      .memories()
      .then((r) => setScopes(r.items))
      .catch((err) => toast.error(`Could not read NextCode's memory: ${errorMessage(err)}`));
  }, []);
  useEffect(load, [load]);
  const nameOf = (scope: string) => (scope === 'global' ? 'Every project' : (projects.find((p) => p.id === scope)?.name ?? `A project (${scope})`));
  const rows = [{ scope: 'global', bytes: 0, updatedAt: '' }, ...(scopes ?? []).filter((s) => s.scope !== 'global')];
  const size = (scope: string) => scopes?.find((s) => s.scope === scope)?.bytes ?? 0;
  return (
    <section className="card settings-card" aria-labelledby="nc-memory">
      <h3 id="nc-memory" className="section-title">
        Memory
      </h3>
      <p className="setting-hint">
        Notes NextCode keeps across conversations, on every account: one for every project and one per project. It reads them when its CLI starts and adds to them itself; you can read and
        change them here. Neither NextBoard’s project memory nor a login’s goes in.
      </p>
      <ul className="nc-accounts" aria-label="NextCode’s memory">
        {rows.map((s) => (
          <li key={s.scope} className="nc-account">
            <Icon name={s.scope === 'global' ? 'globe' : 'folder'} />
            <span className="nc-account-text">
              <span className="nc-account-name">{nameOf(s.scope)}</span>
              <span className="nc-account-detail">{size(s.scope) ? `${Math.max(1, Math.round(size(s.scope) / 1024))} KB of notes` : 'Empty'}</span>
            </span>
            <span className="nc-account-actions">
              <Button size="sm" variant="secondary" onClick={() => setEditing(s.scope)}>
                {size(s.scope) ? 'Open' : 'Write'}
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {editing && (
        <MemoryDialog
          scope={editing}
          title={nameOf(editing)}
          onClose={(saved) => {
            setEditing(null);
            if (saved) load();
          }}
        />
      )}
    </section>
  );
}

function MemoryDialog({ scope, title, onClose }: { scope: string; title: string; onClose: (saved: boolean) => void }) {
  const [text, setText] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    kitApi
      .memory(scope)
      .then((m) => {
        setText(m.text);
        setLoaded(true);
      })
      .catch((err) => setError(errorMessage(err)));
  }, [scope]);
  const save = async () => {
    setBusy(true);
    try {
      await kitApi.putMemory(scope, text);
      onClose(true);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title={`NextCode’s memory · ${title}`}
      icon="brain"
      size="lg"
      onClose={() => onClose(false)}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={() => onClose(false)} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!loaded} onClick={() => void save()}>
            Save
          </Button>
        </>
      }
    >
      <div className="stack">
        <p className="setting-hint">Short notes, one per line. A new conversation reads them; one that runs now from its next start. Never put secrets here.</p>
        <textarea className="control mono nc-kit-text" rows={16} aria-label="Notes" value={text} onChange={(e) => setText(e.target.value)} disabled={!loaded} placeholder="- tests: make test-fast" />
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </Modal>
  );
}

export function McpCard() {
  const settings = useEffectiveSettings();
  const [servers, setServers] = useState<McpServer[] | null>(null);
  useEffect(() => {
    api
      .mcpServers()
      .then(setServers)
      .catch(() => setServers([]));
  }, []);
  const chosen = settings.nextcode?.mcp ?? [];
  const toggle = async (id: string, on: boolean) => {
    const next = on ? [...chosen.filter((x) => x !== id), id] : chosen.filter((x) => x !== id);
    try {
      await patchSettings({ nextcode: { mcp: next.length ? next : null } });
    } catch (err) {
      toast.error(`Could not save NextCode's MCP servers: ${errorMessage(err)}`);
    }
  };
  return (
    <section className="card settings-card" aria-labelledby="nc-mcp">
      <h3 id="nc-mcp" className="section-title">
        MCP servers
      </h3>
      <p className="setting-hint">
        The MCP servers NextCode gets, on every account. None of a login’s, a CLI’s or the account’s connectors (Gmail, Notion, …) come along; only the ones ticked here. Each call of their
        tools asks you first, unless the chat is in Bypass.
      </p>
      {servers === null ? (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading…
        </div>
      ) : servers.length === 0 ? (
        <div className="list-msg">
          No MCP server in NextBoard yet. Add one in <Link to="/settings/mcp">Settings › MCP servers</Link>.
        </div>
      ) : (
        <div className="stack-sm">
          {servers.map((m) => (
            <Checkbox
              key={m.id}
              checked={chosen.includes(m.id)}
              onChange={(v) => void toggle(m.id, v)}
              label={`${m.id}${m.description ? ` · ${m.description}` : ''}${m.enabled ? '' : ' (turned off)'}`}
            />
          ))}
          <p className="setting-hint">
            Add or change servers in <Link to="/settings/mcp">Settings › MCP servers</Link>; one limited to some projects reaches NextCode only there.
          </p>
        </div>
      )}
    </section>
  );
}
