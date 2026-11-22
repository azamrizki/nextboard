// Settings › Notifications (docs/API-agents.md §12): which attention events the hub
// sends (all devices), and how this browser shows them (toast, system notification
// while the tab is in the background — permission asked from the button only). This PC's
// thresholds are here too: they decide its pill's colours and its alert (docs/API.md, Host
// resources).

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { browserPermission, requestBrowserPermission, sendTestNotification, setClientNotifyPrefs, useClientNotifyPrefs, type Permission } from '../agents/attention';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { SettingRow, SwitchRow } from '../components/Switch';
import { toast } from '../components/Toast';

async function save(patch: Record<string, unknown>) {
  try {
    await patchSettings({ notifications: patch });
  } catch (err) {
    toast.error(`Could not save notification settings: ${errorMessage(err)}`);
  }
}

type HostKey = 'warnTempC' | 'hotTempC' | 'warnMemPct' | 'hotMemPct';

const HOST_DEFAULTS: Record<HostKey, number> = { warnTempC: 80, hotTempC: 90, warnMemPct: 85, hotMemPct: 95 };

/** One threshold of This PC: saved on blur or Enter, the hub refuses one out of order. */
function HostNumber({ id, k, value, min, max, unit, label }: { id: string; k: HostKey; value: number; min: number; max: number; unit: string; label: string }) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  const commit = async () => {
    const n = Math.round(Number(draft));
    if (!Number.isFinite(n) || n === value) return setDraft(String(value));
    try {
      await patchSettings({ host: { [k]: Math.max(min, Math.min(max, n)) } });
    } catch (err) {
      toast.error(`Could not save: ${errorMessage(err)}`);
      setDraft(String(value));
    }
  };
  return (
    <div className="input-group has-addon min-sec">
      <input
        id={id}
        className="control control-sm tabular"
        type="number"
        min={min}
        max={max}
        inputMode="numeric"
        aria-label={label}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => void commit()}
        onKeyDown={(e) => e.key === 'Enter' && void commit()}
      />
      <span className="ig-addon ig-text">{unit}</span>
    </div>
  );
}

export function NotificationsPane() {
  const all = useEffectiveSettings();
  const s = all.notifications;
  const host = { ...HOST_DEFAULTS, ...all.host };
  const client = useClientNotifyPrefs();
  const [perm, setPerm] = useState<Permission>(browserPermission());
  const [minSec, setMinSec] = useState(String(s.finishedMinSec));
  useEffect(() => setMinSec(String(s.finishedMinSec)), [s.finishedMinSec]);

  const commitMin = () => {
    const n = Math.max(0, Math.min(3600, Math.round(Number(minSec))));
    if (!Number.isFinite(n)) return setMinSec(String(s.finishedMinSec));
    if (n !== s.finishedMinSec) void save({ finishedMinSec: n });
    setMinSec(String(n));
  };

  const allow = async () => {
    const p = await requestBrowserPermission();
    setPerm(p);
    if (p === 'granted') {
      setClientNotifyPrefs({ browser: true });
      toast.success('System notifications are on for this browser.');
    } else if (p === 'denied') toast.error('The browser blocked notifications. Allow them for this site in the address bar, then try again.');
  };

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="nt-hub">
        <h3 id="nt-hub" className="section-title">
          What to notify about (all devices)
        </h3>
        <SwitchRow title="An agent needs you" hint="A permission request, a question or a plan to approve." checked={s.needsYou} onChange={(v) => void save({ needsYou: v })} />
        <SwitchRow title="An agent finished a long turn" hint="When it goes from working to ready." checked={s.finished} onChange={(v) => void save({ finished: v })} />
        <SettingRow title="Long turn means at least" hint="Shorter turns finish quietly." htmlFor="nt-min">
          <div className="input-group has-addon min-sec">
            <input
              id="nt-min"
              className="control control-sm tabular"
              type="number"
              min={0}
              max={3600}
              inputMode="numeric"
              value={minSec}
              disabled={!s.finished}
              onChange={(e) => setMinSec(e.target.value)}
              onBlur={commitMin}
              onKeyDown={(e) => e.key === 'Enter' && commitMin()}
            />
            <span className="ig-addon ig-text">seconds</span>
          </div>
        </SettingRow>
        <SwitchRow title="Errors" hint="A console failed to start, or an agent exited with an error." checked={s.error} onChange={(v) => void save({ error: v })} />
      </section>

      <section className="card settings-card" aria-labelledby="nt-host">
        <h3 id="nt-host" className="section-title">
          This PC
        </h3>
        <SwitchRow
          title="This PC runs hot or out of memory"
          hint="When it stays over the red line for 30 seconds; once, until it cools down for 5 minutes."
          checked={s.host ?? true}
          onChange={(v) => void save({ host: v })}
        />
        <SettingRow title="Temperature" hint="The hottest of the CPU, the GPUs and the disks. Amber, then red." htmlFor="nt-temp-warn">
          <div className="host-thresholds">
            <HostNumber id="nt-temp-warn" k="warnTempC" value={host.warnTempC} min={40} max={119} unit="°C" label="Amber at" />
            <HostNumber id="nt-temp-hot" k="hotTempC" value={host.hotTempC} min={41} max={120} unit="°C" label="Red at" />
          </div>
        </SettingRow>
        <SettingRow title="Memory" hint="The share programs hold. Amber, then red." htmlFor="nt-mem-warn">
          <div className="host-thresholds">
            <HostNumber id="nt-mem-warn" k="warnMemPct" value={host.warnMemPct} min={50} max={99} unit="%" label="Amber at" />
            <HostNumber id="nt-mem-hot" k="hotMemPct" value={host.hotMemPct} min={51} max={100} unit="%" label="Red at" />
          </div>
        </SettingRow>
      </section>
      <section className="card settings-card" aria-labelledby="nt-client">
        <h3 id="nt-client" className="section-title">
          This browser
        </h3>
        <SwitchRow title="Show toasts" hint="A short note in the corner with a Show button." checked={client.toasts} onChange={(v) => setClientNotifyPrefs({ toasts: v })} />
        {perm === 'granted' ? (
          <SwitchRow
            title="System notifications"
            hint="Only while this tab is in the background or minimised."
            checked={client.browser}
            onChange={(v) => setClientNotifyPrefs({ browser: v })}
          />
        ) : (
          <SettingRow
            title="System notifications"
            hint={
              perm === 'unsupported'
                ? 'This browser has no notification support (or the page isn’t treated as secure).'
                : perm === 'denied'
                  ? 'Blocked for this site. Allow notifications in the browser’s site settings, then reload.'
                  : 'Get a notification while this tab is in the background.'
            }
          >
            <Button size="sm" variant="secondary" icon="bell" onClick={() => void allow()} disabled={perm !== 'default'}>
              Allow notifications
            </Button>
          </SettingRow>
        )}
        {perm === 'granted' && (
          <SettingRow title="Test" hint="Sends one notification now.">
            <Button size="sm" variant="ghost" onClick={() => !sendTestNotification() && toast.error('The browser did not show the notification.')}>
              Send test notification
            </Button>
          </SettingRow>
        )}
      </section>
    </div>
  );
}
