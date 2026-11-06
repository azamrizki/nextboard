// Install app (this device, DESIGN.md §3.17): NextBoard on the home screen in its own window.
// Over plain HTTP a browser does not install, so the pane says what Add to Home screen gives
// there and how to get the install; the hub keeps working over HTTP either way.

import { useState } from 'react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { SettingRow } from '../components/Switch';
import { useInstall } from '../lib/install';

export function AppPane() {
  const { state, install } = useInstall();
  const [declined, setDeclined] = useState(false);
  const origin = window.location.origin;

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="app-device">
        <h3 id="app-device" className="section-title">
          This device
        </h3>
        {state === 'installed' && (
          <SettingRow title="Installed" hint="NextBoard runs as an app here: its own icon and window, without the browser's bars.">
            <Icon name="check" className="app-installed" />
          </SettingRow>
        )}
        {state === 'ready' && (
          <SettingRow
            title="Install NextBoard"
            hint={declined ? 'Not installed. The browser may offer it again later, here or in its menu.' : 'Its own icon on the home screen and its own window, without the browser’s bars.'}
          >
            <Button variant="primary" icon="download" onClick={() => void install().then((ok) => setDeclined(!ok))}>
              Install
            </Button>
          </SettingRow>
        )}
        {state === 'manual' && (
          <>
          <SettingRow title="Install from the browser’s menu" hint="This browser has not offered the install here. It may be installed already, or the browser wants the page used a little longer first.">
            {null}
          </SettingRow>
          <ul className="app-steps">
            <li>
              <b>Chrome on Android:</b> ⋮ → <i>Add to Home screen</i> → <i>Install</i>.
            </li>
            <li>
              <b>Chrome or Edge on a computer:</b> the install icon at the end of the address bar.
            </li>
            <li>
              <b>Safari:</b> Share → <i>Add to Home Screen</i>.
            </li>
          </ul>
          </>
        )}
        {state === 'insecure' && (
          <>
            <div className="alert warning" role="note">
              <Icon name="alert" />
              <span>
                This hub is open over plain HTTP (<span className="mono">{origin}</span>). Browsers install apps only from HTTPS, so <i>Add to Home screen</i> gives a shortcut that
                opens in a browser tab, with the browser’s bars. Everything else works the same.
              </span>
            </div>
            <p className="setting-hint">To install it as an app, either:</p>
            <ul className="app-steps">
              <li>
                open the hub over HTTPS: nginx in front of it (<span className="mono">deploy/nginx.conf.example</span>), or <span className="mono">tailscale serve</span>, which
                leaves out web tiles; or
              </li>
              <li>
                in Chrome on a device of your own, add <span className="mono">{origin}</span> under <span className="mono">chrome://flags</span> → <i>Insecure origins treated as secure</i>,
                then restart Chrome.
              </li>
            </ul>
          </>
        )}
      </section>
    </div>
  );
}
