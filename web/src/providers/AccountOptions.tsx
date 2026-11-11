// What a console starts with, in one select: the default login, another login profile, or an API
// provider (docs/API-agents.md §10.2). Profiles are pf_…, providers ap_…, the default "".

import { defaultLoginLabel } from '../agents/settings';
import type { Profile } from '../lib/types';
import type { ApiProvider } from './providers';

export function isProviderId(v: string | undefined): boolean {
  return !!v && v.startsWith('ap_');
}

/** A select value as the API takes it. */
export function splitAccount(v: string): { profileId?: string; providerId?: string } {
  if (!v) return {};
  return isProviderId(v) ? { providerId: v } : { profileId: v };
}

export function AccountOptions({ profiles, defaultProfile, providers }: { profiles: Pick<Profile, 'id' | 'name'>[]; defaultProfile: string; providers: ApiProvider[] }) {
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
