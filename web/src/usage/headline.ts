// The usage a glance shows (the header's usage pill, the browser tab): the current session's
// window of the default CLI on a machine, from the last check this hub keeps.

import { useEffect } from 'react';
import { useEffectiveSettings } from '../agents/settings';
import { studioStore, usageKey, useStudio } from '../studio/store';
import type { UsageReport, UsageWindow } from '../studio/types';
import { sessionWindow } from './format';
import { usageAgentOn, USAGE_AGENTS } from './prefs';

export interface Headline {
  /** The CLI it is of: the default one when its usage is on, else the first that is. */
  agent: string;
  /** Its short name ("Claude"). */
  name: string;
  /** The CLIs whose usage is on. */
  enabled: string[];
  report: UsageReport | undefined;
  /** The session window of a report that read fine. */
  top: UsageWindow | undefined;
  /** Both CLIs are turned off in Settings › Agents: nothing to show usage of. */
  none: boolean;
}

export function useHeadlineUsage(machineId: string): Headline {
  const settings = useEffectiveSettings();
  const reports = useStudio((s) => s.usage);
  const loaded = useStudio((s) => s.usageLoaded);
  const enabled = USAGE_AGENTS.filter((a) => usageAgentOn(settings, a.id)).map((a) => a.id as string);
  const def = (settings.runner as { defaultAgent?: string } | undefined)?.defaultAgent ?? 'claude';
  const agent = enabled.find((a) => a === def) ?? enabled[0] ?? USAGE_AGENTS[0].id;
  const report = reports[usageKey({ machineId, agent, profileId: '' })];

  useEffect(() => {
    if (!loaded) void studioStore.loadUsage();
  }, [loaded]);

  return {
    agent,
    name: USAGE_AGENTS.find((a) => a.id === agent)?.short ?? agent,
    enabled,
    report,
    top: report?.state === 'ok' ? sessionWindow(report.windows) : undefined,
    none: USAGE_AGENTS.every((a) => settings.agents.disabled?.[a.id]),
  };
}
