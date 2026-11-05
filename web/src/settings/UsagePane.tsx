// Settings › Usage (F2–F4, docs/API-studio.md §10, §12). What a usage check does to the
// machine is spelled out next to each switch, and the safe choice is the default until
// the owner decides (decision #2 is still open): no automatic checks in this browser,
// Codex checks off. The hub-wide switches (usage.background, usage.agents, chime) apply to
// every device.

import { errorMessage } from '../api';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { SettingRow, SwitchRow } from '../components/Switch';
import { toast } from '../components/Toast';
import { playChime } from '../usage/chime';
import { setUsagePrefs, usageAgentOn, useUsagePrefs } from '../usage/prefs';

async function save(patch: Record<string, unknown>) {
  try {
    await patchSettings({ usage: patch });
  } catch (err) {
    toast.error(`Could not save usage settings: ${errorMessage(err)}`);
  }
}

export function UsagePane() {
  const prefs = useUsagePrefs();
  const s = useEffectiveSettings();
  const usage = s.usage ?? { background: true, chime: { enabled: true, play: 'after100' as const, sound: 'bell' as const } };
  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="us-checks">
        <h3 id="us-checks" className="section-title">
          When to check
        </h3>
        <p className="setting-hint">
          A check opens the CLI for a few seconds in an empty temporary folder on that machine (/tmp/nb-usage-…, removed afterwards), reads its usage screen and closes it.
          It never sends a prompt. Until you change it, checks only run when you click the usage pill or Check again.
        </p>
        <SwitchRow
          title="Check automatically in this browser"
          hint="When a board opens and the last check is over 3 minutes old. Each automatic check has the same side effects as a manual one (below), so they add up over a day."
          checked={prefs.auto}
          onChange={(v) => {
            setUsagePrefs({ auto: v });
            if (v && !usage.background) void save({ background: true });
          }}
        />
        <SwitchRow
          title="Allow automatic checks on this hub"
          hint="Hub-wide switch for every device (apps included). Off: the hub refuses automatic checks and only runs the ones you ask for."
          checked={usage.background}
          onChange={(v) => {
            void save({ background: v });
            if (!v && prefs.auto) setUsagePrefs({ auto: false });
          }}
        />
      </section>
      <section className="card settings-card" aria-labelledby="us-cli">
        <h3 id="us-cli" className="section-title">
          Which CLIs to check (all devices)
        </h3>
        <SwitchRow
          title="Claude Code"
          hint="Runs /usage. Claude Code records every command it is given, so each check adds 2 lines (/usage and /exit) to the history.jsonl of that login."
          checked={usageAgentOn(s, 'claude')}
          disabled={!!s.agents.disabled?.claude}
          onChange={(v) => void save({ agents: { claude: v } })}
        />
        <SwitchRow
          title="Codex"
          hint="Runs /status, without sending a prompt. Starting Codex interactively makes Codex 0.165 write a [tui] block (animations = false, screen_reader_detection_done = true) into the config.toml of that login when it isn’t there yet. Off by default for that reason."
          checked={usageAgentOn(s, 'codex')}
          disabled={!!s.agents.disabled?.codex}
          onChange={(v) => void save({ agents: { codex: v } })}
        />
      </section>
      <section className="card settings-card" aria-labelledby="us-chime">
        <h3 id="us-chime" className="section-title">
          Chime when usage resets (all devices)
        </h3>
        <SwitchRow title="Play a chime" hint="Shortly after a usage window resets, open NextBoard tabs play a sound and show a note." checked={usage.chime.enabled} onChange={(v) => void save({ chime: { enabled: v } })} />
        <SettingRow title="Play" hint="Only after a window was used up, or after every reset.">
          <SegmentedControl<'after100' | 'always'>
            value={usage.chime.play}
            onChange={(v) => void save({ chime: { play: v } })}
            size="sm"
            label="Play"
            disabled={!usage.chime.enabled}
            options={[
              { value: 'after100', label: 'Only after 100%' },
              { value: 'always', label: 'Always' },
            ]}
          />
        </SettingRow>
        <SettingRow title="Sound" htmlFor="us-sound">
          <div className="inline-controls">
            <select id="us-sound" className="control control-sm" value={usage.chime.sound} disabled={!usage.chime.enabled} onChange={(e) => void save({ chime: { sound: e.target.value } })}>
              <option value="bell">Bell</option>
              <option value="chime">Chime</option>
              <option value="pop">Pop</option>
              <option value="soft">Soft</option>
            </select>
            <Button size="sm" variant="ghost" icon="volume" onClick={() => !playChime(usage.chime.sound) && toast.info('Click anywhere on the page once, then try again (browsers block sound until then).')}>
              Test
            </Button>
          </div>
        </SettingRow>
      </section>
    </div>
  );
}
