// Ask the hub for a usage report (§10.1): a fresh enough cached one comes back at once,
// otherwise the hub runs a short hidden check (a job whose result is the report).

import { isCode } from '../api';
import { orchStore } from '../orch/store';
import { isJob, usageApi } from '../studio/api';
import { studioStore, waitJob } from '../studio/store';
import type { UsageReport } from '../studio/types';
import { USAGE_AGENTS } from './prefs';

const inflight = new Map<string, Promise<UsageReport | null>>();

export function checkUsage(o: { machineId: string; agent: string; profileId?: string; maxAgeSec: number; trigger: 'user' | 'background' }): Promise<UsageReport | null> {
  const k = `${o.machineId}|${o.agent}|${o.profileId ?? ''}|${o.trigger}`;
  const cur = inflight.get(k);
  if (cur) return cur;
  const p = (async () => {
    try {
      const r = await usageApi.check({ machineId: o.machineId, agent: o.agent, profileId: o.profileId || undefined, maxAgeSec: o.maxAgeSec, trigger: o.trigger });
      if (!isJob(r)) {
        studioStore.upsertUsage(r);
        return r;
      }
      orchStore.upsertJob(r);
      const done = await waitJob(r.id, 120_000);
      const rep = done.result as UsageReport | null;
      if (rep && typeof rep === 'object' && 'state' in rep) {
        studioStore.upsertUsage(rep);
        return rep;
      }
      throw new Error(done.error || 'the check did not finish');
    } catch (err) {
      // background checks switched off on the hub: not an error for the user
      if (o.trigger === 'background' && (isCode(err, 'background_disabled') || isCode(err, 'usage_disabled'))) return null;
      if (isCode(err, 'usage_disabled')) throw new Error(`${USAGE_AGENTS.find((a) => a.id === o.agent)?.name ?? o.agent} checks are off. Turn them on in Settings › Usage.`);
      throw err;
    } finally {
      inflight.delete(k);
    }
  })();
  inflight.set(k, p);
  return p;
}
