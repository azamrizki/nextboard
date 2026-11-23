// Keeps the browser tab's title and icon up to date (tab/status.ts). Mounted once while signed in,
// on every page but print; gone, it leaves the tab as it found it.

import { useEffect, useState } from 'react';
import { isActive, isIdle } from '../agents/state';
import { setTitleStatus } from '../lib/title';
import type { Session } from '../lib/types';
import { useEventsConnected, useRunningCount, useStoreReady } from '../store';
import { resetsShort } from '../usage/format';
import { useHeadlineUsage } from '../usage/headline';
import { faviconHref, statusText, USAGE_FRESH_MS, useTabPrefs, type TabFacts } from './status';

/** Agent CLIs run on This PC: its usage is the one the header shows. */
const USAGE_MACHINE = 'local';

let original: string | null = null;

/** The tab's icon: href, or "" for the one index.html has. */
function setFavicon(href: string) {
  const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (!link) return;
  if (original === null) original = link.getAttribute('href') ?? '';
  const next = href || original;
  if (link.getAttribute('href') !== next) link.setAttribute('href', next);
}

// idle and working count agents only: a shell at its prompt is not one that finished
const needsYouOf = (s: Session) => s.agentState === 'needsYou';
const idleOf = (s: Session) => isIdle(s) && s.agentState === 'ready';
const workingOf = (s: Session) => isActive(s) && s.agentState !== 'shell';

export function TabStatus({ projectId }: { projectId?: string }) {
  const prefs = useTabPrefs();
  const only = prefs.scope === 'project' ? (projectId ?? '') : '';
  const mine = (s: Session) => !only || s.projectId === only;
  const needsYou = useRunningCount((s) => mine(s) && needsYouOf(s));
  const idle = useRunningCount((s) => mine(s) && idleOf(s));
  const working = useRunningCount((s) => mine(s) && workingOf(s));
  const connected = useEventsConnected();
  const ready = useStoreReady();
  const { report, top } = useHeadlineUsage(USAGE_MACHINE);
  const [now, setNow] = useState(() => Date.now());

  // a report ages out of the tab without anything else changing
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(t);
  }, []);

  const checked = report ? Date.parse(report.checkedAt) : NaN;
  const fresh = !!top && Number.isFinite(checked) && now - checked < USAGE_FRESH_MS;
  const facts: TabFacts = {
    needsYou,
    idle,
    working,
    usage: fresh && top ? { pct: Math.round(top.usedPct), resets: top.resetsText ? resetsShort(top.resetsText) : '' } : null,
    offline: ready && !connected,
  };
  const text = prefs.on ? statusText(facts, prefs.title) : '';
  const icon = prefs.on && prefs.icon ? faviconHref(facts) : '';

  useEffect(() => setTitleStatus(text), [text]);
  useEffect(() => setFavicon(icon), [icon]);
  useEffect(
    () => () => {
      setTitleStatus('');
      setFavicon('');
    },
    [],
  );
  return null;
}
