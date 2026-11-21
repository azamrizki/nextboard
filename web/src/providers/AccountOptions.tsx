// What a console starts with, in one select: the default login, another login profile, or an API
// provider (docs/API-agents.md §10.2). Profiles are pf_…, providers ap_…, the default "".
// NextCode's API engine (§30) has no login: its select lists the API providers only.

import { defaultLoginLabel } from '../agents/settings';
import { Link } from '../lib/router';
import type { Profile } from '../lib/types';
import { PROVIDERS_PATH } from '../nextcode/engines';
import type { ApiProvider } from './providers';

export { accountFor, isProviderId, needsProvider, splitAccount } from './accounts';

interface Props {
  profiles: Pick<Profile, 'id' | 'name'>[];
  defaultProfile: string;
  providers: ApiProvider[];
  /** NextCode's API engine: the providers alone, no login. */
  providersOnly?: boolean;
  /** Settings › NextCode's default provider, marked "(default)" in a providers-only list. */
  defaultProvider?: string;
}

export function AccountOptions({ profiles, defaultProfile, providers, providersOnly = false, defaultProvider }: Props) {
  if (providersOnly)
    return (
      <>
        {providers.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
            {p.id === defaultProvider ? ' (default)' : ''}
          </option>
        ))}
      </>
    );
  return (
    <>
      <option value="">{defaultLoginLabel(profiles, defaultProfile)}</option>
      {profiles
        .filter((p) => p.id !== defaultProfile)
        .map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      {providers.length > 0 && (
        <optgroup label="API providers">
          {providers.map((p) => (
            <option key={p.id} value={p.id}>
              API · {p.name}
            </option>
          ))}
        </optgroup>
      )}
    </>
  );
}

/** Where the account select would be, for the API engine with no provider yet: Start waits for one. */
export function NeedsProviderHint({ className = '' }: { className?: string }) {
  return (
    <p className={`field-hint nc-needs-provider ${className}`} role="note">
      NextCode’s API engine runs on an API provider, and there is none yet. Add one in <Link to={PROVIDERS_PATH}>Settings › API providers</Link>.
    </p>
  );
}
