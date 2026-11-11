// Settings › Lead agent (docs/API-orchestration.md §3.9): the orchestrator's name, how it
// talks and leads, and whether it talks before the team gets to work. A card can have its
// own name and personality.

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { useMachineAgents } from '../agents/catalog';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { SettingRow, SwitchRow } from '../components/Switch';
import { compatible, useProviders, type ApiProvider } from '../providers/providers';
import { toast } from '../components/Toast';
import { leadNameOf } from '../orch/lead';
import { LeadAvatar } from '../orch/OrderBits';
import '../styles/orch.css';

async function save(patch: Record<string, unknown>) {
  try {
    await patchSettings({ lead: patch });
  } catch (err) {
    toast.error(`Could not save: ${errorMessage(err)}`);
  }
}

export function LeadPane() {
  const s = useEffectiveSettings();
  const name = leadNameOf(s);
  const [draftName, setDraftName] = useState(name);
  const [soul, setSoul] = useState(s.lead?.personality ?? '');
  useEffect(() => setDraftName(name), [name]);
  useEffect(() => setSoul(s.lead?.personality ?? ''), [s.lead?.personality]);
  const talkFirst = s.lead?.talkFirst !== false;
  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="lead-who">
        <h3 id="lead-who" className="section-title">
          Who leads your agents
        </h3>
        <p className="setting-hint">
          The lead is your assistant and the agent who directs the others. It runs a CLI on This PC (below), so talking to it uses that CLI’s plan or API provider. Name a card on the board to give that card a lead of its own.
        </p>
        <div className="lead-who">
          <LeadAvatar name={draftName || name} size="lg" />
          <div className="field grow">
            <label className="field-label" htmlFor="lead-name">
              Name
            </label>
            <input
              id="lead-name"
              className="control"
              value={draftName}
              maxLength={24}
              onChange={(e) => setDraftName(e.target.value)}
              onBlur={() => {
                const v = draftName.trim();
                if (v && v !== name) void save({ name: v });
                else setDraftName(name);
              }}
            />
          </div>
        </div>
        <div className="field">
          <label className="field-label" htmlFor="lead-soul">
            Personality
          </label>
          <textarea
            id="lead-soul"
            className="control lead-soul"
            rows={4}
            maxLength={800}
            value={soul}
            placeholder="How it talks and leads. Calm and to the point; pushes back when a plan looks risky."
            onChange={(e) => setSoul(e.target.value)}
            onBlur={() => {
              if (soul.trim() !== (s.lead?.personality ?? '')) void save({ personality: soul.trim() });
            }}
          />
        </div>
      </section>
      <LeadRunner />
      <section className="card settings-card" aria-labelledby="lead-how">
        <h3 id="lead-how" className="section-title">
          How it works with you
        </h3>
        <SwitchRow
          title="Talk first"
          hint={`${name} talks with you and asks when something is unclear. Work goes to the agents right away; ${name} only waits for your OK when it can destroy something, like deleting files or data. Off: ${name} carries out what you ask without asking back. Either way, an order that names an agent (@Arkan) and plain commands like “pause everyone” go straight out.`}
          checked={talkFirst}
          onChange={(v) => void save({ talkFirst: v })}
        />
      </section>
    </div>
  );
}

/** The CLI a hidden run takes on a provider: Claude Code on an Anthropic API, Codex on the Responses API. */
function runnerCli(p: ApiProvider): string {
  if (compatible({ id: 'claude', base: '' }, p)) return 'Claude Code';
  if (compatible({ id: 'codex', base: '' }, p)) return 'Codex';
  return '';
}

/** What the lead thinks with, and what the consoles it starts run on (docs/API-agents.md §10.2). */
function LeadRunner() {
  const s = useEffectiveSettings();
  const providers = useProviders();
  const { agents } = useMachineAgents('local');
  const name = leadNameOf(s);
  const runner = s.lead?.runner ?? '';
  const hidden = providers.find((p) => p.id === s.runner?.provider);
  const fits = providers.filter((p) => runnerCli(p));
  const known = runner === '' || runner === 'claude' || runner === 'codex' || fits.some((p) => p.id === runner);
  const picks = s.lead?.providers ?? {};
  const rows = agents.filter((a) => a.kind === 'agent' && providers.some((p) => compatible(a, p)));
  return (
    <section className="card settings-card" aria-labelledby="lead-runs">
      <h3 id="lead-runs" className="section-title">
        What it runs on
      </h3>
      <SettingRow
        title="Thinks with"
        hint={`${name}’s own turns: planning, splitting results, summaries. On a login they use that plan; on an API provider its budget counts them. Codex needs an OpenAI provider with the Responses API.`}
      >
        <select className="control control-sm" aria-label="Lead runs on" value={runner} onChange={(e) => void save({ runner: e.target.value || null })}>
          <option value="">Like hidden runs · Claude Code{hidden && runnerCli(hidden) === 'Claude Code' ? ` on API · ${hidden.name}` : ''}</option>
          <option value="claude">Claude Code · its login</option>
          <option value="codex">Codex · its login</option>
          {fits.length > 0 && (
            <optgroup label="API providers">
              {fits.map((p) => (
                <option key={p.id} value={p.id}>
                  API · {p.name} ({runnerCli(p)})
                </option>
              ))}
            </optgroup>
          )}
          {!known && <option value={runner}>A removed provider</option>}
        </select>
      </SettingRow>
      {rows.length > 0 ? (
        rows.map((a) => {
          const opts = providers.filter((p) => compatible(a, p));
          const v = picks[a.id] ?? '';
          return (
            <SettingRow key={a.id} title={`New ${a.name} consoles`} hint={`What a ${a.name} console ${name} starts runs on. An order that names a provider (“start two Claude on the API”) or “login” wins.`}>
              <select className="control control-sm" aria-label={`${a.name} consoles the lead starts`} value={v} onChange={(e) => void save({ providers: { [a.id]: e.target.value || null } })}>
                <option value="">Its login</option>
                {opts.map((p) => (
                  <option key={p.id} value={p.id}>
                    API · {p.name}
                  </option>
                ))}
                {v && !opts.some((p) => p.id === v) && <option value={v}>A removed provider</option>}
              </select>
            </SettingRow>
          );
        })
      ) : (
        <p className="setting-hint">Consoles {name} starts use their login. Add an API provider in Settings › API providers to run them there instead.</p>
      )}
    </section>
  );
}
