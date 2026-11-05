// Settings › Lead agent (docs/API-orchestration.md §3.9): the orchestrator's name, how it
// talks and leads, and whether it talks before the team gets to work. A card can have its
// own name and personality.

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { SwitchRow } from '../components/Switch';
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
          The lead is your assistant and the agent who directs the others. It runs on Claude Code on This PC, so talking to it uses your plan. Name a card on the board to give that card a lead of its own.
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
