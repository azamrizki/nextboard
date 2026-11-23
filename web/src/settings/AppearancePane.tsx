// Appearance (per browser): light or dark, the layout, glass or solid surfaces, what the
// connection chip shows, and the terminal text size.

import { setGlobalTextScale, TEXT_STEPS, useGlobalTextScale } from '../agents/textSize';
import { SegmentedControl } from '../components/form';
import { SettingRow, SwitchRow } from '../components/Switch';
import { setLayoutPref, useLayout, type LayoutPref } from '../lib/layout';
import { setConsolePref, setGlassPref, setThemePref, useTheme, type ConsolePref, type GlassPref, type ThemePref } from '../lib/theme';
import { setLatencyShown, useLatencyShown } from '../net/latency';
import { DOWN_BYTES, runSpeedTest, stopSpeedTest, UP_BYTES, useSpeedTest } from '../net/speedTest';

const THEMES: { value: ThemePref; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

const LAYOUTS: { value: LayoutPref; label: string }[] = [
  { value: 'auto', label: 'Auto' },
  { value: 'phone', label: 'Phone' },
  { value: 'tablet', label: 'Tablet' },
  { value: 'desktop', label: 'Desktop' },
];

const LAYOUT_NAME = { phone: 'phone', tablet: 'tablet', desktop: 'desktop' } as const;

const GLASS: { value: GlassPref; label: string }[] = [
  { value: 'full', label: 'Glass' },
  { value: 'solid', label: 'Solid' },
];

const CONSOLES: { value: ConsolePref; label: string }[] = [
  { value: 'match', label: 'Match the theme' },
  { value: 'dark', label: 'Always dark' },
];

/** Runs the speed test and says its last result; the details are in the header chip's popover. */
function SpeedTestButton() {
  const s = useSpeedTest();
  const running = s.phase === 'down' || s.phase === 'up';
  return (
    <span className="speed-test-row">
      <span className="speed-test-result tabular" role="status">
        {running
          ? `${s.phase === 'down' ? 'Download' : 'Upload'} ${s.progress?.mbps ?? 0} Mbps…`
          : s.phase === 'error'
            ? s.error
            : s.down && s.up
              ? `↓ ${s.down.mbps} · ↑ ${s.up.mbps} Mbps`
              : ''}
      </span>
      {running ? (
        <button type="button" className="btn btn-secondary btn-sm" onClick={stopSpeedTest}>
          Stop
        </button>
      ) : (
        <button type="button" className="btn btn-secondary btn-sm" onClick={() => void runSpeedTest()}>
          {s.down ? 'Test again' : 'Test speed'}
        </button>
      )}
    </span>
  );
}

export function AppearancePane() {
  const scale = useGlobalTextScale();
  const { pref, consolePref, glass } = useTheme();
  const shown = useLayout();
  const latency = useLatencyShown();
  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="appearance-theme">
        <h3 id="appearance-theme" className="section-title">
          Theme
        </h3>
        <SettingRow title="Mode" hint="System follows this device's light or dark setting. The Memory sky stays dark in every mode.">
          <SegmentedControl<ThemePref> label="Theme" size="sm" value={pref} onChange={setThemePref} options={THEMES} />
        </SettingRow>
        <SettingRow
          title="Layout"
          hint={`Auto picks from the screen: a phone under 600 px, a tablet under 1200 px or on any touch screen, a desktop otherwise. This browser shows the ${LAYOUT_NAME[shown.layout]} layout${shown.pref === 'auto' ? '' : ', as chosen here'}.`}
        >
          <SegmentedControl<LayoutPref> label="Layout" size="sm" value={shown.pref} onChange={setLayoutPref} options={LAYOUTS} />
        </SettingRow>
        <SettingRow title="Surfaces" hint="Glass lets the backdrop show softly through panels and cards. Solid draws them opaque, which is faster on an older device. A system setting to reduce transparency always gives Solid.">
          <SegmentedControl<GlassPref> label="Surfaces" size="sm" value={glass} onChange={setGlassPref} options={GLASS} />
        </SettingRow>
      </section>
      <section className="card settings-card" aria-labelledby="appearance-net">
        <h3 id="appearance-net" className="section-title">
          Connection to the hub
        </h3>
        <SwitchRow
          title="Show the round trip in the header"
          hint="Signal bars and the milliseconds to the hub, measured every 15 seconds over the live connection with a few bytes. Off, the chip says Live as before. Click it for the details and the speed test."
          checked={latency}
          onChange={setLatencyShown}
        />
        <SettingRow title="Speed test" hint={`Download and upload speed between this browser and the hub. It runs only when asked: about ${DOWN_BYTES >> 20} MB down and ${UP_BYTES >> 20} MB up, mind a phone's data.`}>
          <SpeedTestButton />
        </SettingRow>
      </section>
      <section className="card settings-card" aria-labelledby="appearance-text">
        <h3 id="appearance-text" className="section-title">
          Terminals
        </h3>
        <SettingRow title="Colours" hint="For a CLI set up for a dark terminal. In a light console NextBoard darkens text that would be too faint to read.">
          <SegmentedControl<ConsolePref> label="Terminal colours" size="sm" value={consolePref} onChange={setConsolePref} options={CONSOLES} />
        </SettingRow>
        <SettingRow title="Text size" hint="Applies to every console in this browser. A console can override it from its menu (Text −/+).">
          <SegmentedControl
            label="Terminal text size"
            size="sm"
            value={String(scale)}
            onChange={(v) => setGlobalTextScale(Number(v))}
            options={TEXT_STEPS.map((n) => ({ value: String(n), label: `${n}%` }))}
            className="text-size-seg"
          />
        </SettingRow>
      </section>
    </div>
  );
}
