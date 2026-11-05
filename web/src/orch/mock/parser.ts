// DEV MOCK ONLY (nb.orchMock): a small subset of the hub's local parser
// (docs/API-orchestration.md §3.2) so the web UI can be exercised before a hub implements
// Phase 6. The real grammar, resolution and policies live in the hub.

export type Parsed =
  | { op: 'send'; who: string[]; text: string; verbatim: boolean }
  | { op: 'spawn'; count: number; agent?: string; role?: string; group?: { name?: string } }
  | { op: 'groupThese' }
  | { op: 'pause' | 'resume' | 'kill' | 'restart'; who: string[]; all?: boolean }
  | { op: 'ask'; query: 'status' | 'finished' | 'help' | 'summary' | 'needsYou' }
  | { op: 'ui'; action: string; panel?: string; target?: string; tool?: string }
  | { op: 'loop'; everySec: number; text: string }
  | { op: 'memoryPush'; who: string[] }
  | { op: 'memoryScan' }
  | { op: 'deploy'; name: string }
  | { op: 'forward'; from: string[]; to: string[] }
  | { op: 'handoff'; agent: string; from?: string }
  | { op: 'nodeAdd'; kind: 'orchestrator' | 'note' | 'kanban' | 'checklist' | 'calendar'; text?: string }
  | { op: 'cancel' }
  | { op: 'smalltalk' }
  | { op: 'bareMention'; who: string }
  | { op: 'unknown'; text: string };

const NUM: Record<string, number> = { a: 1, an: 1, one: 1, two: 2, three: 3, four: 4, five: 5, six: 6, seven: 7, eight: 8, nine: 9, ten: 10, eleven: 11, twelve: 12 };
const num = (w: string | undefined): number | undefined => (w === undefined ? undefined : /^\d+$/.test(w) ? Number(w) : NUM[w.toLowerCase()]);
const ROLES = ['frontend', 'backend', 'testers', 'tester', 'reviewers', 'reviewer', 'design', 'designers', 'docs', 'security', 'general'];

/** Names in "@Ash and @Bo", "Ash, Bo", "group 2", "the testers". */
function names(s: string): string[] {
  return s
    .split(/\s*(?:,|\band\b|&)\s*/i)
    .map((x) => x.trim().replace(/^@/, '').replace(/^the\s+/i, ''))
    .filter(Boolean);
}

export function parse(raw: string, knownAgents: string[]): Parsed {
  const text = raw.trim().replace(/\s+/g, ' ');
  const t = text.toLowerCase();
  if (/^(thanks|thank you|ok|okay|nice|cool|great)[.!]*$/.test(t)) return { op: 'smalltalk' };
  if (/^(cancel that|never mind|nevermind|scratch that)$/.test(t)) return { op: 'cancel' };
  if (/^(status|how'?s it going\??|who needs me\??)$/.test(t)) return { op: 'ask', query: t.startsWith('who needs') ? 'needsYou' : 'status' };
  if (/^(who finished|what happened)\??$/.test(t)) return { op: 'ask', query: 'finished' };
  if (/^(help|what can you do\??)$/.test(t)) return { op: 'ask', query: 'help' };
  if (/^give me a summary$/.test(t)) return { op: 'ask', query: 'summary' };
  if (/^(show me everything|fit)$/.test(t)) return { op: 'ui', action: 'fit' };
  if (t === 'zoom in') return { op: 'ui', action: 'zoomIn' };
  if (t === 'zoom out') return { op: 'ui', action: 'zoomOut' };
  if (t === 'reset zoom') return { op: 'ui', action: 'zoomReset' };
  if (t === 'undo' || t === 'redo') return { op: 'ui', action: t };
  if (t === 'pen' || t === 'i want to draw') return { op: 'ui', action: 'tool', tool: 'pen' };
  if (t === 'eraser') return { op: 'ui', action: 'tool', tool: 'eraser' };
  let m = /^open (loops|memory|mission map|settings|git|usage|documents|code|markdown|skills)$/.exec(t);
  if (m) return { op: 'ui', action: 'openPanel', panel: m[1] === 'mission map' ? 'mission' : m[1] };
  if (t === 'close that') return { op: 'ui', action: 'closePanel' };
  m = /^(?:go to|focus on) (.+)$/.exec(text);
  if (m && !/^workspace/i.test(m[1])) return { op: 'ui', action: 'focus', target: m[1].replace(/^@/, '') };
  if (/^push memory$/.test(t)) return { op: 'memoryPush', who: [] };
  m = /^push memory to (.+)$/i.exec(text);
  if (m) return { op: 'memoryPush', who: names(m[1]) };
  if (t === 'scan memory') return { op: 'memoryScan' };
  m = /^deploy (.+)$/i.exec(text);
  if (m) return { op: 'deploy', name: m[1].trim() };
  m = /^every (\w+) (minutes?|mins?|hours?) (.+)$/i.exec(text);
  if (m) {
    const n = num(m[1]) ?? 0;
    return { op: 'loop', everySec: n * (m[2].toLowerCase().startsWith('h') ? 3600 : 60), text: m[3] };
  }
  m = /^when (.+?) finish(?:es)?,? (?:pass|send|forward) (?:it|everything|the results?) to (.+)$/i.exec(text);
  if (m) return { op: 'forward', from: names(m[1]), to: names(m[2]) };
  m = /^(?:hand off|pass)(?: this(?: task)?)? to (\S+)(?: from @?(\S+))?$/i.exec(text);
  if (m) return { op: 'handoff', agent: m[1].toLowerCase(), from: m[2] };
  if (/^(pause|wait|hold on)$/.test(t)) return { op: 'pause', who: [], all: true };
  if (/^(resume|continue|keep going)$/.test(t)) return { op: 'resume', who: [], all: true };
  m = /^(pause|resume|continue|restart|reset|kill|close|stop) (.+)$/i.exec(text);
  if (m) {
    const verb = m[1].toLowerCase();
    const op = verb === 'pause' ? 'pause' : verb === 'resume' || verb === 'continue' ? 'resume' : verb === 'restart' || verb === 'reset' ? 'restart' : 'kill';
    if (/^all$/i.test(m[2])) return { op, who: [], all: true };
    return { op, who: names(m[2]) };
  }
  if (/^group these$/.test(t)) return { op: 'groupThese' };
  m = /^(?:make|create) a group of (\w+)(?: (\S+))?(?: agents?)?(?: called (.+))?$/i.exec(text);
  if (m) {
    const count = num(m[1]) ?? 3;
    const w = m[2]?.toLowerCase();
    const agent = w && knownAgents.includes(w) ? w : undefined;
    const role = w && ROLES.includes(w) ? w : undefined;
    return { op: 'spawn', count, agent, role, group: { name: m[3] } };
  }
  m = /^(?:spawn|spin up|start|launch)(?: (\w+))?(?: (\S+))?(?: (\S+))?$/i.exec(text);
  if (m) {
    const count = num(m[1]) ?? 1;
    const words = [num(m[1]) === undefined ? m[1] : undefined, m[2], m[3]].filter(Boolean).map((x) => x!.toLowerCase());
    const agent = words.find((w) => knownAgents.includes(w) || knownAgents.includes(w.replace(/s$/, '')));
    const role = words.find((w) => ROLES.includes(w));
    return { op: 'spawn', count: Math.min(12, count), agent: agent?.replace(/s$/, '') && knownAgents.includes(agent.replace(/s$/, '')) ? agent.replace(/s$/, '') : agent, role };
  }
  if (/^(new console|open a (?:shell|terminal))$/.test(t)) return { op: 'spawn', count: 1, agent: t.includes('shell') || t.includes('terminal') ? 'shell' : undefined };
  m = /^add an? (orchestrator|note|kanban|checklist|calendar)(?: that says (.+))?$/i.exec(text);
  if (m) return { op: 'nodeAdd', kind: m[1].toLowerCase() as 'orchestrator', text: m[2] };
  m = /^(?:give everyone the same prompt|tell everyone):\s*(.+)$/i.exec(text);
  if (m) return { op: 'send', who: ['everyone'], text: m[1], verbatim: true };
  m = /^(?:tell|ask) @?(.+?) to (.+)$/i.exec(text);
  if (m) return { op: 'send', who: names(m[1]), text: m[2], verbatim: false };
  // @mentions at the start: "@Ash @Bo run the tests"
  m = /^((?:@[\w-]+[,\s]*(?:and\s+)?)+)(.*)$/i.exec(text);
  if (m) {
    const who = [...m[1].matchAll(/@([\w-]+)/g)].map((x) => x[1]);
    const rest = m[2].trim();
    if (!rest) return { op: 'bareMention', who: who[0] };
    if (who.length === 1 && /^(everyone|all|everybody)$/i.test(who[0])) return { op: 'send', who: [], text: rest, verbatim: true };
    return { op: 'send', who, text: rest, verbatim: true };
  }
  m = /^([A-Z][\w-]*), (.+)$/.exec(text);
  if (m) return { op: 'send', who: [m[1]], text: m[2], verbatim: false };
  return { op: 'unknown', text };
}

/** Damerau-Levenshtein distance (optimal string alignment). */
export function editDistance(a: string, b: string): number {
  const d: number[][] = Array.from({ length: a.length + 1 }, (_, i) => [i, ...Array(b.length).fill(0)]);
  for (let j = 1; j <= b.length; j++) d[0][j] = j;
  for (let i = 1; i <= a.length; i++)
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      d[i][j] = Math.min(d[i - 1][j] + 1, d[i][j - 1] + 1, d[i - 1][j - 1] + cost);
      if (i > 1 && j > 1 && a[i - 1] === b[j - 2] && a[i - 2] === b[j - 1]) d[i][j] = Math.min(d[i][j], d[i - 2][j - 2] + 1);
    }
  return d[a.length][b.length];
}
