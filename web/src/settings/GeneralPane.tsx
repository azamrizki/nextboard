// General (hub-wide, docs/API-agents.md §11): where opening a project goes, its board or its Code
// view, or a question each time (DESIGN.md §3.19). Kept on the hub, so every browser and phone
// follows it; the "Remember my choice" of the question writes the same setting.

import { useState } from 'react';
import { errorMessage } from '../api';
import { patchSettings } from '../agents/settings';
import { RadioCards } from '../components/form';
import { toast } from '../components/Toast';
import { useOpenProjectsIn } from '../home/OpenChoice';
import type { OpenProjectsIn } from '../lib/types';

const CHOICES: { value: OpenProjectsIn; title: string; description: string }[] = [
  { value: '', title: 'Ask every time', description: 'Opening a project asks: Board view or Code view.' },
  { value: 'board', title: 'Board view', description: 'Consoles, agents, cards and the lead on one canvas.' },
  { value: 'code', title: 'Code view', description: 'A full code editor: files, terminals, agents and the lead in one window.' },
];

// RadioCards keys its options by value, and "" is a value of its own here
const ASK = 'ask';
type Choice = typeof ASK | 'board' | 'code';
const toChoice = (v: OpenProjectsIn): Choice => (v === '' ? ASK : v);
const fromChoice = (c: Choice): OpenProjectsIn => (c === ASK ? '' : c);

export function GeneralPane() {
  const value = useOpenProjectsIn();
  const [saving, setSaving] = useState(false);

  const change = async (c: Choice) => {
    const v = fromChoice(c);
    if (v === value || saving) return;
    setSaving(true);
    try {
      await patchSettings({ ui: { openProjectsIn: v } });
    } catch (err) {
      toast.error(`Could not save: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="general-open">
        <h3 id="general-open" className="section-title">
          Open projects in
        </h3>
        <RadioCards<Choice>
          labelledBy="general-open"
          columns={1}
          value={toChoice(value)}
          disabled={saving}
          onChange={(c) => void change(c)}
          options={CHOICES.map((o) => ({ value: toChoice(o.value), title: o.title, description: o.description }))}
        />
        <p className="setting-hint">Kept on the hub, so every browser and phone follows it. A project card’s menu always offers both views.</p>
      </section>
    </div>
  );
}
