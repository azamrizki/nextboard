// Usage pill in the header (F2, docs/API-studio.md §10.1): the current session's window of
// the default CLI on This PC, where agent CLIs run. Clicking it opens the popover and asks for a
// report (a cached one under 30 s old is reused). Nothing runs by itself unless this
// browser turned automatic checks on in Settings › Usage.

import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { useEffectiveSettings } from '../agents/settings';
import { AgentMark } from './AgentMark';
import { checkUsage } from './check';
import { untilShort } from './format';
import { useHeadlineUsage } from './headline';
import { usageAgentOn, usageLevel, usageUi, useUsageOpen, useUsagePrefs } from './prefs';

const UsagePopover = lazy(() => import('./UsagePopover'));

export function UsagePill({ machineId }: { machineId: string }) {
  const open = useUsageOpen();
  const prefs = useUsagePrefs();
  const settings = useEffectiveSettings();
  const ref = useRef<HTMLButtonElement>(null);
  const { agent, name, enabled, top, none } = useHeadlineUsage(machineId);
  const on = enabled.join();

  // automatic checks (opt-in): when a board opens, if the last check is older than 3 min
  useEffect(() => {
    if (!prefs.auto || !on) return;
    for (const a of on.split(',')) void checkUsage({ machineId, agent: a, maxAgeSec: 180, trigger: 'background' }).catch(() => undefined);
  }, [machineId, prefs.auto, on]);

  // the current session's window, as the CLI's own /usage leads with it
  const level = top ? usageLevel(top.usedPct) : 'ok';
  const label = top ? `${name} ${top.label}: ${top.usedPct}% used${top.resetsText ? `, ${top.resetsText.toLowerCase()}` : ''}` : `${name} usage`;

  // the time left in the window ("2h"), as the agreed header has it; the clock time is in the tip
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!top?.resetsAt) return;
    const t = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(t);
  }, [top?.resetsAt]);
  const left = untilShort(top?.resetsAt, now);

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
  if (none) return null;

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
            {left && <span className="usage-pill-reset tabular">{left}</span>}
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
