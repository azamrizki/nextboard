// Usage popover (F2, F4): one tab per CLI and login profile. The first window the CLI printed
// as a ring, the others as bars (amber ≥ 75 %, red ≥ 90 %) with their reset times, the facts
// it printed, the raw screen on demand; below them a year of activity and a few numbers from
// the CLI's own transcripts. Checks are best effort: the hub reads the CLI's usage screen.

import { lazy, Suspense, useEffect, useMemo, useState, type RefObject } from 'react';
import { errorMessage } from '../api';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Popover } from '../components/Popover';
import { toast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import { navigate } from '../lib/router';
import { useMachines, useProfiles } from '../store';
import { usageApi } from '../studio/api';
import { usageKey, useStudio } from '../studio/store';
import type { TokenChart as Chart, UsageReport, UsageWindow } from '../studio/types';
import { ActivityMap } from './ActivityMap';
import { AgentMark } from './AgentMark';
import { checkUsage } from './check';
import { compact, dayLabel, durationShort, modelName } from './format';
import { usageAgentOn, usageLevel, USAGE_AGENTS } from './prefs';

const TokenChart = lazy(() => import('./TokenChart'));

interface Props {
  anchor: RefObject<HTMLElement | null>;
  machineId: string;
  initialAgent: string;
  onClose: () => void;
}

interface Chip {
  key: string;
  agent: string;
  profileId: string;
  label: string;
}

/** What the CLI's own usage screen is called, for the footer. */
const USAGE_COMMAND: Record<string, string> = { claude: '/usage', codex: '/status' };
const ACTIVITY_DAYS = 366;
const ACTIVITY_TTL = 5 * 60_000;

const SIDE_EFFECT: Record<string, string> = {
  claude: 'Each check runs /usage in Claude Code inside a temporary empty folder and adds two lines (/usage, /exit) to the history.jsonl of that login.',
  codex: 'Each check starts Codex in a temporary empty folder and runs /status, without sending a prompt. Codex 0.165 then writes a [tui] block into the config.toml of that login if it isn’t there yet.',
};

// A year of transcripts takes a moment to add up: reopening the popover shouldn't wait for it.
const activityCache = new Map<string, { at: number; chart: Chart }>();

function useActivity(machineId: string, agent: string) {
  const key = `${machineId}|${agent}`;
  const [state, setState] = useState<{ key: string; chart: Chart | null; error: string }>(() => ({ key, chart: activityCache.get(key)?.chart ?? null, error: '' }));
  useEffect(() => {
    const c = activityCache.get(key);
    setState({ key, chart: c?.chart ?? null, error: '' });
    if (c && Date.now() - c.at < ACTIVITY_TTL) return;
    let stop = false;
    usageApi
      .tokens(ACTIVITY_DAYS, machineId, agent)
      .then((chart) => {
        // still adding up: the next look should ask again
        if (!chart.partial) activityCache.set(key, { at: Date.now(), chart });
        if (!stop) setState({ key, chart, error: '' });
      })
      .catch((err) => !stop && setState({ key, chart: null, error: errorMessage(err) }));
    return () => {
      stop = true;
    };
  }, [key, machineId, agent]);
  return state.key === key ? state : { key, chart: null, error: '' };
}

const NO_AGENTS: Record<string, boolean> = {};

export default function UsagePopover({ anchor, machineId: initialMachine, initialAgent, onClose }: Props) {
  const settings = useEffectiveSettings();
  const machines = useMachines().filter((m) => m.status === 'connected' || m.status === 'local');
  const [machineId, setMachineId] = useState(initialMachine);
  const allProfiles = useProfiles();
  const defaults = settings.agents.profiles?.[machineId];
  const off = settings.agents.disabled ?? NO_AGENTS;
  const chips: Chip[] = useMemo(() => {
    const out: Chip[] = [];
    for (const a of USAGE_AGENTS) {
      if (off[a.id]) continue;
      out.push({ key: `${a.id}|`, agent: a.id, profileId: '', label: a.short });
      // the plain chip already is the default profile: the same account, not a second chip
      for (const p of allProfiles.filter((x) => x.machineId === machineId && x.agent === a.id && x.id !== defaults?.[a.id])) {
        out.push({ key: `${a.id}|${p.id}`, agent: a.id, profileId: p.id, label: `${a.short} · ${p.name}` });
      }
    }
    return out;
  }, [allProfiles, machineId, defaults, off]);
  const [sel, setSel] = useState(`${initialAgent}|`);
  const chip = chips.find((c) => c.key === sel) ?? chips[0];
  const rep = useStudio((s) => s.usage[usageKey({ machineId, agent: chip.agent, profileId: chip.profileId })]);
  const [busy, setBusy] = useState(false);
  const [raw, setRaw] = useState(false);
  const [chart, setChart] = useState(false);
  const allowed = usageAgentOn(settings, chip.agent);

  const check = async (maxAgeSec: number) => {
    setBusy(true);
    try {
      await checkUsage({ machineId, agent: chip.agent, profileId: chip.profileId, maxAgeSec, trigger: 'user' });
    } catch (err) {
      toast.error(`Usage check failed: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  // switching chips is a click too: reuse a report under 30 s old, else check
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (touched && allowed) void check(30);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel, machineId]);

  const name = USAGE_AGENTS.find((a) => a.id === chip.agent)?.name ?? chip.agent;

  const turnOn = async () => {
    setBusy(true);
    try {
      await patchSettings({ usage: { agents: { [chip.agent]: true } } });
    } catch (err) {
      toast.error(`Could not turn on ${name} checks: ${errorMessage(err)}`);
      setBusy(false);
      return;
    }
    await check(30);
  };

  return (
    <Popover anchor={anchor} onClose={onClose} label="Usage" className="usage-pop" width={420}>
      <div className="usage-head" data-agent={chip.agent}>
        <h2>Usage</h2>
        <div className="usage-tabs" role="tablist" aria-label="CLI and login">
          {chips.map((c) => {
            const off = !usageAgentOn(settings, c.agent);
            const profile = c.label.split(' · ')[1];
            return (
              <button
                key={c.key}
                type="button"
                role="tab"
                aria-selected={c.key === chip.key}
                aria-label={`${c.label}${off ? ', checks off' : ''}`}
                title={`${c.label}${off ? ' · checks off' : ''}`}
                className={`usage-tab ${c.key === chip.key ? 'is-active' : ''} ${off ? 'is-off' : ''}`}
                onClick={() => {
                  setTouched(true);
                  setSel(c.key);
                }}
              >
                <AgentMark agent={c.agent} />
                {profile && <span className="truncate">{profile}</span>}
              </button>
            );
          })}
        </div>
        <span className="spacer" />
        {!!rep?.raw?.length && (
          <Button size="sm" variant="ghost" iconOnly icon="terminal" aria-label={raw ? 'Parsed view' : 'Raw screen'} title={raw ? 'Parsed view' : 'Raw screen'} aria-pressed={raw} onClick={() => setRaw((r) => !r)} />
        )}
        <Button size="sm" variant="ghost" iconOnly icon="chartBar" aria-label="Token history" title="Token history" onClick={() => setChart(true)} />
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon="sliders"
          aria-label="Usage settings"
          title="Usage settings"
          onClick={() => {
            onClose();
            navigate('/settings/usage');
          }}
        />
        {allowed && <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Check again" title="Check again" loading={busy} onClick={() => void check(0)} />}
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close usage" onClick={onClose} />
      </div>
      {machines.length > 1 && (
        <select
          className="control control-sm usage-machine"
          aria-label="Machine"
          value={machineId}
          onChange={(e) => {
            setTouched(true);
            setMachineId(e.target.value);
          }}
        >
          {machines.map((m) => (
            <option key={m.id} value={m.id}>
              {m.name}
            </option>
          ))}
        </select>
      )}

      {!allowed ? (
        <div className="usage-off">
          <p className="fs-sm">{name} checks are off on this hub.</p>
          <p className="fs-xs text-3">{SIDE_EFFECT[chip.agent]}</p>
          <div className="row">
            <Button size="sm" variant="primary" icon="power" loading={busy} onClick={() => void turnOn()}>
              Turn on and check
            </Button>
            <Button
              size="sm"
              variant="ghost"
              icon="sliders"
              onClick={() => {
                onClose();
                navigate('/settings/usage');
              }}
            >
              Usage settings
            </Button>
          </div>
        </div>
      ) : (
        <div className="usage-report" data-agent={chip.agent}>
          <ReportView report={rep} name={name} busy={busy} raw={raw} />
        </div>
      )}

      <ActivitySection machineId={machineId} agent={chip.agent} />

      <div className="usage-foot" data-agent={chip.agent}>
        <span className="usage-cmd mono">
          <AgentMark agent={chip.agent} />
          {chip.agent}
          {USAGE_COMMAND[chip.agent] && (
            <>
              <span className="text-3"> › </span>
              {USAGE_COMMAND[chip.agent]}
            </>
          )}
        </span>
        <span className="spacer" />
        {allowed && !rep && !busy && (
          <Button size="sm" variant="primary" icon="refresh" onClick={() => void check(0)}>
            Check now
          </Button>
        )}
        {allowed && (rep || busy) && (
          <span className="fs-xs text-3">{busy ? 'Refreshing…' : `Checked ${timeAgo(rep!.checkedAt)}${rep!.trigger === 'background' ? ' automatically' : ''}`}</span>
        )}
      </div>
      {allowed && !rep && <p className="fs-xs text-3 usage-note">{SIDE_EFFECT[chip.agent]}</p>}

      {chart && (
        <Suspense fallback={null}>
          <TokenChart machineId={machineId} onClose={() => setChart(false)} />
        </Suspense>
      )}
    </Popover>
  );
}

function ReportView({ report, name, busy, raw }: { report: UsageReport | undefined; name: string; busy: boolean; raw: boolean }) {
  if (!report) return <p className="list-msg">{busy ? `Reading ${name}’s usage screen…` : `No usage report for ${name} yet.`}</p>;
  if (raw && report.raw?.length)
    return (
      <pre className="usage-raw nowheel" aria-label="Raw usage screen">
        {report.raw.join('\n')}
      </pre>
    );
  const [first, ...rest] = report.windows ?? [];
  return (
    <>
      {report.state !== 'ok' && (
        <div className="usage-state">
          <p className={`fs-sm ${report.state === 'error' ? 'text-danger' : 'text-warning'}`}>
            <Icon name="alertCircle" />{' '}
            {report.state === 'notLoggedIn'
              ? `${name} isn’t signed in on this machine. Sign in once in a console, then check again.`
              : report.state === 'needsInteractive'
                ? `${name} stopped on a question it can’t answer by itself. Start it once in a console, answer it, then check again.`
                : report.state === 'unsupported'
                  ? report.error
                    ? `${name} has no usage limits for this login.`
                    : `${name} has no usage screen NextBoard can read.`
                  : report.error
                    ? `Check failed: ${report.error}`
                    : 'The usage screen wasn’t recognised.'}
          </p>
        </div>
      )}
      {first && <SessionRing w={first} />}
      {rest.length > 0 && (
        <ul className="plain usage-windows" aria-label="Usage windows">
          {rest.map((w) => (
            <WindowBar key={w.id || w.label} w={w} />
          ))}
        </ul>
      )}
      {!!report.facts?.length && (
        <dl className="kv usage-facts">
          {report.facts.map((f) => (
            <div key={f.label}>
              <dt>{f.label}</dt>
              <dd className="truncate">{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </>
  );
}

/** The level in words too: colour alone doesn't say it. */
function LevelBadge({ pct }: { pct: number }) {
  const lvl = usageLevel(pct);
  if (lvl === 'ok') return null;
  return (
    <span className={`badge ${lvl === 'high' ? 'badge-danger' : 'badge-warning'}`}>
      <Icon name="alert" /> {lvl === 'high' ? 'Almost out' : 'High'}
    </span>
  );
}

const resetTitle = (w: UsageWindow) => (w.resetsAt ? `In ${until(w.resetsAt)}` : undefined);

function SessionRing({ w }: { w: UsageWindow }) {
  const pct = Math.min(100, Math.max(0, w.usedPct));
  return (
    <div className={`usage-ring-row lvl-${usageLevel(w.usedPct)}`}>
      <div className="usage-ring" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={w.usedPct} aria-label={`${w.label} used`}>
        <svg viewBox="0 0 64 64" aria-hidden="true">
          <circle className="usage-ring-track" cx="32" cy="32" r="28" pathLength={100} />
          <circle className="usage-ring-value" cx="32" cy="32" r="28" pathLength={100} strokeDasharray={`${pct} 100`} />
        </svg>
        <span className="usage-ring-text">
          <strong className="tabular">{w.usedPct}%</strong>
          <span>used</span>
        </span>
      </div>
      <div className="usage-ring-info">
        <div className="usage-window-row">
          <strong>{w.label}</strong>
          <LevelBadge pct={w.usedPct} />
        </div>
        {w.resetsText && (
          <span className="usage-reset mono" title={resetTitle(w)}>
            {w.resetsText}
          </span>
        )}
      </div>
    </div>
  );
}

function WindowBar({ w }: { w: UsageWindow }) {
  return (
    <li className={`usage-window lvl-${usageLevel(w.usedPct)}`}>
      <div className="usage-window-row">
        <strong>{w.label}</strong>
        <span className="spacer" />
        <LevelBadge pct={w.usedPct} />
        <strong className="tabular">{w.usedPct}%</strong>
      </div>
      <div className="usage-bar" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={w.usedPct} aria-label={`${w.label} used`}>
        <span style={{ width: `${Math.min(100, Math.max(0, w.usedPct))}%` }} />
      </div>
      {w.resetsText && (
        <span className="usage-reset mono" title={resetTitle(w)}>
          {w.resetsText}
        </span>
      )}
    </li>
  );
}

function ActivitySection({ machineId, agent }: { machineId: string; agent: string }) {
  const { chart, error } = useActivity(machineId, agent);
  const days = useMemo(() => {
    const m = new Map<string, number>();
    for (const p of chart?.series ?? []) m.set(p.date, (m.get(p.date) ?? 0) + p.input + p.output + p.cacheRead + p.cacheWrite);
    return m;
  }, [chart]);
  const totals = chart?.byAgent?.find((a) => a.agent === agent);
  const active = [...days.keys()].sort();
  // active days out of those since the first one, not out of a year nobody used it in
  const span = active.length ? Math.round((Date.parse(chart!.to) - Date.parse(active[0])) / 86_400_000) + 1 : 0;
  return (
    <section className="usage-activity" data-agent={agent} aria-label="Activity" title="Counted from the CLI’s own transcripts on this machine. This is not a bill.">
      <div className="usage-section-head">
        <h3>Activity</h3>
        <span className="spacer" />
        <span className="fs-xs text-3">{chart?.partial ? 'Still counting…' : 'Last 12 months'}</span>
      </div>
      {error && <p className="fs-xs text-3">Activity isn’t available: {error}</p>}
      {!error && !chart && (
        <p className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Reading transcripts…
        </p>
      )}
      {chart && (
        <>
          <ActivityMap from={chart.from} to={chart.to} days={days} />
          <div className="activity-scale" aria-hidden="true">
            Less
            {[0, 1, 2, 3, 4].map((n) => (
              <span key={n} className={`activity-key key-${n}`} />
            ))}
            More
          </div>
          <dl className="usage-stats">
            <div>
              <dt>Favorite model</dt>
              <dd title={totals?.favoriteModel}>{totals?.favoriteModel ? modelName(totals.favoriteModel) : '—'}</dd>
            </div>
            <div>
              <dt>Total tokens</dt>
              <dd className="tabular">{totals?.allTime.total ? compact(totals.allTime.total) : '—'}</dd>
            </div>
            <div>
              <dt>Sessions</dt>
              <dd className="tabular">{totals?.sessions ? totals.sessions.toLocaleString() : '—'}</dd>
            </div>
            <div>
              <dt>Active days</dt>
              <dd className="tabular">{span ? `${active.length}/${span}` : '—'}</dd>
            </div>
            <div>
              <dt>Most active day</dt>
              <dd>{chart.busiestDay?.total ? dayLabel(chart.busiestDay.date) : '—'}</dd>
            </div>
            <div>
              <dt>Longest session</dt>
              <dd className="tabular">{durationShort(totals?.longestSessionSec ?? 0)}</dd>
            </div>
          </dl>
        </>
      )}
    </section>
  );
}

function until(iso: string): string {
  const ms = Date.parse(iso) - Date.now();
  if (!(ms > 0)) return 'a moment';
  const m = Math.round(ms / 60000);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h ${m % 60} min`;
  return `${Math.round(h / 24)} days`;
}
