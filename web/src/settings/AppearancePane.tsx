// Appearance (per browser): light or dark, the layout, glass or solid surfaces, and the terminal text size.

import { setGlobalTextScale, TEXT_STEPS, useGlobalTextScale } from '../agents/textSize';
import { SegmentedControl } from '../components/form';
import { SettingRow } from '../components/Switch';
import { setLayoutPref, useLayout, type LayoutPref } from '../lib/layout';
import { setConsolePref, setGlassPref, setThemePref, useTheme, type ConsolePref, type GlassPref, type ThemePref } from '../lib/theme';

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

export function AppearancePane() {
  const scale = useGlobalTextScale();
  const { pref, consolePref, glass } = useTheme();
  const shown = useLayout();
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
