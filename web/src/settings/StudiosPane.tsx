// Settings › Previews & studios (docs/API-studio.md §12): automatic localhost previews,
// the preview port range (read when the hub starts), the CLI for hidden runs (documents,
// diagrams, scans) and AI commit subjects.

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { SegmentedControl } from '../components/form';
import { SettingRow, SwitchRow } from '../components/Switch';
import { toast } from '../components/Toast';
import { Button } from '../components/Button';
import { ManagedBrowserCard } from './ManagedBrowserCard';
import { compatible, useProviders } from '../providers/providers';

async function save(patch: Record<string, unknown>, what: string) {
  try {
    await patchSettings(patch);
  } catch (err) {
    toast.error(`Could not save ${what}: ${errorMessage(err)}`);
  }
}

export function StudiosPane() {
  const s = useEffectiveSettings();
  const previews = s.previews ?? { auto: true, portRange: [12320, 12339] as [number, number] };
  const [lo, setLo] = useState(String(previews.portRange[0]));
  const [hi, setHi] = useState(String(previews.portRange[1]));
  useEffect(() => {
    setLo(String(previews.portRange[0]));
    setHi(String(previews.portRange[1]));
  }, [previews.portRange]);
  const a = Number(lo);
  const b = Number(hi);
  const rangeOk = Number.isInteger(a) && Number.isInteger(b) && a >= 1024 && b <= 65535 && b >= a && b - a < 200;
  const changed = a !== previews.portRange[0] || b !== previews.portRange[1];
  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="st-prev">
        <h3 id="st-prev" className="section-title">
          Web previews (all devices)
        </h3>
        <SwitchRow
          title="Open previews by themselves"
          hint="When a console prints a localhost address and something answers there, a web tile opens next to it, joined by a cyan line."
          checked={previews.auto}
          onChange={(v) => void save({ previews: { auto: v } }, 'preview settings')}
        />
        <SettingRow title="Preview ports" hint={`Each preview gets its own HTTPS port on the hub (1024–65535, at most 200). Takes effect after the hub restarts. Open these ports in the firewall when you use NextBoard from other devices.`}>
          <div className="inline-controls">
            <input className="control control-sm tabular port-input" inputMode="numeric" aria-label="First port" value={lo} onChange={(e) => setLo(e.target.value)} />
            <span className="text-3">–</span>
            <input className="control control-sm tabular port-input" inputMode="numeric" aria-label="Last port" value={hi} onChange={(e) => setHi(e.target.value)} />
            <Button size="sm" disabled={!changed || !rangeOk} onClick={() => void save({ previews: { portRange: [a, b] } }, 'the port range').then(() => toast.info('Saved. The new range is used after the hub restarts.'))}>
              Save
            </Button>
          </div>
        </SettingRow>
      </section>
      <ManagedBrowserCard />
      <section className="card settings-card" aria-labelledby="st-run">
        <h3 id="st-run" className="section-title">
          Hidden runs (all devices)
        </h3>
        <p className="setting-hint">Documents, diagrams and memory scans run your own CLI headless and read-only in the project folder, with your login and plan.</p>
        <RunnerProvider agent={s.runner?.defaultAgent ?? 'claude'} value={s.runner?.provider ?? ''} />
        <SettingRow title="Default CLI">
          <SegmentedControl<string>
            value={s.runner?.defaultAgent ?? 'claude'}
            onChange={(v) => void save({ runner: { defaultAgent: v } }, 'the default CLI')}
            size="sm"
            label="Default CLI"
            options={[
              { value: 'claude', label: 'Claude Code' },
              { value: 'codex', label: 'Codex' },
            ]}
          />
        </SettingRow>
        <SwitchRow
          title="Write commit messages with the CLI"
          hint="On: the sparkle button has your CLI read the diffs of the changed files and write a Conventional Commits message (one run without tools, on your plan, usually under a minute). Secret files such as .env are never sent. Off: an instant one-line subject from the file names, in the same form."
          checked={s.git?.commitWithRunner !== false}
          onChange={(v) => void save({ git: { commitWithRunner: v } }, 'the Git setting')}
        />
      </section>
    </div>
  );
}

/** Hidden runs through an API provider: the lead, memory, summaries, commit messages, documents. */
function RunnerProvider({ agent, value }: { agent: string; value: string }) {
  const providers = useProviders();
  // the lead and the thread summaries run Claude Code (unless Settings › Lead agent picks theirs); the rest the default CLI
  const fits = providers.filter((p) => compatible({ id: agent, base: '' }, p) || compatible({ id: 'claude', base: '' }, p));
  if (!providers.length && !value) return null;
  return (
    <SettingRow
      title="Run through"
      hint="An API provider instead of the CLI’s login, for every hidden run its CLI can make: the lead agent and thread summaries (Claude Code, so an Anthropic-compatible provider, unless Settings › Lead agent picks what the lead runs on), and the rest with the default CLI above. Its budget counts them."
    >
      <select className="control control-sm" aria-label="Run hidden runs through" value={value} onChange={(e) => void save({ runner: { provider: e.target.value || null } }, 'hidden runs')}>
        <option value="">The CLI’s login</option>
        {fits.map((p) => (
          <option key={p.id} value={p.id}>
            API · {p.name}
          </option>
        ))}
        {value && !fits.some((p) => p.id === value) && <option value={value}>A removed provider</option>}
      </select>
    </SettingRow>
  );
}
