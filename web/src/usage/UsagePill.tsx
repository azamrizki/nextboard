// Usage pill in the header (F2, docs/API-studio.md §10.1): the busiest window of the
// default CLI on the project's machine. Clicking it opens the popover and asks for a
// report (a cached one under 30 s old is reused). Nothing runs by itself unless this
// browser turned automatic checks on in Settings › Usage.

import { lazy, Suspense, useEffect, useRef } from 'react';
import { useEffectiveSettings } from '../agents/settings';
import { studioStore, usageKey, useStudio } from '../studio/store';
import { AgentMark } from './AgentMark';
import { checkUsage } from './check';
import { resetsShort } from './format';
import { usageAgentOn, usageLevel, usageUi, useUsageOpen, useUsagePrefs, USAGE_AGENTS } from './prefs';

const UsagePopover = lazy(() => import('./UsagePopover'));

export function UsagePill({ machineId }: { machineId: string }) {
  const open = useUsageOpen();
  const prefs = useUsagePrefs();
  const settings = useEffectiveSettings();
  const ref = useRef<HTMLButtonElement>(null);
  const reports = useStudio((s) => s.usage);
  const loaded = useStudio((s) => s.usageLoaded);
  const enabled = USAGE_AGENTS.filter((a) => usageAgentOn(settings, a.id)).map((a) => a.id as string);
  const on = enabled.join();
  const def = (settings.runner as { defaultAgent?: string } | undefined)?.defaultAgent ?? 'claude';
  const agent = enabled.find((a) => a === def) ?? enabled[0] ?? USAGE_AGENTS[0].id;
  const rep = reports[usageKey({ machineId, agent, profileId: '' })];

  useEffect(() => {
    if (!loaded) void studioStore.loadUsage();
  }, [loaded]);

  // automatic checks (opt-in): when a board opens, if the last check is older than 3 min
  useEffect(() => {
    if (!prefs.auto || !on) return;
    for (const a of on.split(',')) void checkUsage({ machineId, agent: a, maxAgeSec: 180, trigger: 'background' }).catch(() => undefined);
  }, [machineId, prefs.auto, on]);

  const top = rep?.state === 'ok' ? [...(rep.windows ?? [])].sort((a, b) => b.usedPct - a.usedPct)[0] : undefined;
  const name = USAGE_AGENTS.find((a) => a.id === agent)?.short ?? agent;
  const level = top ? usageLevel(top.usedPct) : 'ok';
  const label = top ? `${name} ${top.label}: ${top.usedPct}% used${top.resetsText ? `, ${top.resetsText.toLowerCase()}` : ''}` : `${name} usage`;

  const reset = top?.resetsText ? resetsShort(top.resetsText) : '';

  const toggle = () => {
    if (open) {
      usageUi.set(false);
      return;
    }
    usageUi.set(true);
    // clicking is the user asking: check (reusing a report under 30 s old)
    if (usageAgentOn(settings, agent)) void checkUsage({ machineId, agent, maxAgeSec: 30, trigger: 'user' }).catch(() => undefined);
  };

  // nothing to show usage of: both CLIs are turned off in Settings › Agents
  if (USAGE_AGENTS.every((a) => settings.agents.disabled?.[a.id])) return null;

  return (
    <>
      <button
        ref={ref}
        type="button"
        className={`usage-pill lvl-${level} ${open ? 'is-open' : ''}`}
        data-agent={agent}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={`${label}. Open usage`}
        title={label}
        onClick={toggle}
      >
        <AgentMark agent={agent} />
        {top ? (
          <>
            <span className="tabular usage-pill-pct">{top.usedPct}%</span>
            {reset && <span className="usage-pill-reset">{reset}</span>}
          </>
        ) : (
          <span className="usage-pill-name">{name}</span>
        )}
      </button>
      {open && (
        <Suspense fallback={null}>
          <UsagePopover anchor={ref} machineId={machineId} initialAgent={agent} onClose={() => usageUi.set(false)} />
        </Suspense>
      )}
    </>
  );
}
