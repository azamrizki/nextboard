// Settings › Notifications (docs/API-agents.md §12): which attention events the hub
// sends (all devices), and how this browser shows them (toast, system notification
// while the tab is in the background — permission asked from the button only).

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

export function NotificationsPane() {
  const s = useEffectiveSettings().notifications;
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
