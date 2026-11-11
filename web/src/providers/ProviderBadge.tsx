// The badge of a console on an API provider (docs/API-agents.md §10.2): which provider, and what
// its conversation has cost so far at the provider's prices.

import { useEffect, useState } from 'react';
import { Icon } from '../components/Icon';
import { money, providersApi, tokensShort, useProvider, type Spend } from './providers';

const EVERY_MS = 60_000;

export function ProviderBadge({ sessionId, providerId }: { sessionId: string; providerId: string }) {
  const p = useProvider(providerId);
  const [spend, setSpend] = useState<Spend | null>(null);
  useEffect(() => {
    let live = true;
    const load = () =>
      providersApi
        .sessionCost(sessionId)
        .then((s) => live && setSpend(s))
        .catch(() => undefined);
    void load();
    const t = window.setInterval(load, EVERY_MS);
    return () => {
      live = false;
      window.clearInterval(t);
    };
  }, [sessionId]);
  const name = p?.name ?? 'a removed provider';
  const used = spend ? spend.tokens.input + spend.tokens.cacheRead + spend.tokens.cacheWrite : 0;
  const detail = spend
    ? ` · ${tokensShort(used)} in, ${tokensShort(spend.tokens.output)} out · ${spend.cost > 0 || !spend.unpriced?.length ? money(spend.cost) : 'no price set'}${spend.unpriced?.length && spend.cost > 0 ? ' (some models have no price)' : ''}`
    : '';
  return (
    <span className="badge badge-info node-badge provider-badge" title={`API provider: ${name}${detail}`} aria-label={`API provider ${name}${spend ? `, ${money(spend.cost)} so far` : ''}`}>
      <Icon name="plug" />
      API{spend && spend.cost > 0 ? ` · ${money(spend.cost)}` : ''}
    </span>
  );
}
