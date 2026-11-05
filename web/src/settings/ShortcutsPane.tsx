// Settings › Shortcuts (docs/API-agents.md §11): single-key board tools. Click a key and
// press a new one; Esc keeps the old key, Delete restores the default. X (Mission Map)
// and Space (push-to-talk) are fixed. Clashes are flagged before saving.

import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { errorMessage } from '../api';
import { ACTIVE_SHORTCUTS, DEFAULT_SHORTCUTS, patchSettings, SHORTCUT_LABELS, shortcutProblem, useShortcuts } from '../agents/settings';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import type { ShortcutMap } from '../lib/types';

type Action = keyof ShortcutMap;

const LATER: Action[] = (Object.keys(DEFAULT_SHORTCUTS) as Action[]).filter((a) => !ACTIVE_SHORTCUTS.includes(a));

export function ShortcutsPane() {
  const map = useShortcuts();
  const [busy, setBusy] = useState(false);
  const custom = (Object.keys(map) as Action[]).some((a) => map[a] !== DEFAULT_SHORTCUTS[a]);
  const resetAll = async () => {
    setBusy(true);
    try {
      await patchSettings({ shortcuts: null });
      toast.success('Shortcuts restored to the defaults.');
    } catch (err) {
      toast.error(`Could not reset shortcuts: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="sc-board">
        <div className="settings-card-head">
          <h3 id="sc-board" className="section-title">
            Board tools
          </h3>
          <Button size="sm" variant="ghost" icon="restart" onClick={() => void resetAll()} loading={busy} disabled={!custom}>
            Restore defaults
          </Button>
        </div>
        <p className="setting-hint">Click a key, then press the new one. Esc keeps the current key; Delete restores the default.</p>
        <ul className="plain shortcut-list">
          {ACTIVE_SHORTCUTS.map((a) => (
            <ShortcutRow key={a} action={a} map={map} />
          ))}
          <li className="shortcut-row is-fixed">
            <span className="shortcut-label">Mission Map</span>
            <span className="shortcut-note">Fixed</span>
            <kbd className="keycap">X</kbd>
          </li>
          <li className="shortcut-row is-fixed">
            <span className="shortcut-label">Push-to-talk (hold)</span>
            <span className="shortcut-note">Fixed</span>
            <kbd className="keycap">Space</kbd>
          </li>
        </ul>
      </section>
      {LATER.length > 0 && (
      <section className="card settings-card" aria-labelledby="sc-later">
        <h3 id="sc-later" className="section-title">
          Coming in later updates
        </h3>
        <p className="setting-hint">These keys are reserved now so they don’t clash later.</p>
        <ul className="plain shortcut-list">
          {LATER.map((a) => (
            <ShortcutRow key={a} action={a} map={map} />
          ))}
        </ul>
      </section>
      )}
    </div>
  );
}

function ShortcutRow({ action, map }: { action: Action; map: ShortcutMap }) {
  const [capturing, setCapturing] = useState(false);
  const [problem, setProblem] = useState('');
  const [saving, setSaving] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const key = map[action];
  const isDefault = key === DEFAULT_SHORTCUTS[action];

  useEffect(() => {
    if (!capturing) return;
    const blur = () => setCapturing(false);
    const el = btn.current;
    el?.addEventListener('blur', blur);
    return () => el?.removeEventListener('blur', blur);
  }, [capturing]);

  const store = async (value: string | null) => {
    setSaving(true);
    try {
      await patchSettings({ shortcuts: { [action]: value } });
      setProblem('');
    } catch (err) {
      setProblem(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (!capturing) return;
    if (e.key === 'Tab') return;
    e.preventDefault();
    e.stopPropagation();
    if (e.key === 'Escape') {
      setCapturing(false);
      setProblem('');
      return;
    }
    if (e.key === 'Delete' || e.key === 'Backspace') {
      setCapturing(false);
      if (!isDefault) void store(null);
      return;
    }
    if (e.ctrlKey || e.metaKey || e.altKey || e.key.length !== 1) return;
    const k = e.key.length === 1 && /[A-Z]/.test(e.key) ? e.key.toLowerCase() : e.key;
    const p = shortcutProblem(k, action, map);
    if (p) {
      setProblem(p);
      return;
    }
    setCapturing(false);
    if (k !== key) void store(k);
  };

  const id = `sc-${action}`;
  return (
    <li className={`shortcut-row ${problem ? 'has-error' : ''}`}>
      <span className="shortcut-label" id={`${id}-l`}>
        {SHORTCUT_LABELS[action]}
      </span>
      <span className="shortcut-note" id={`${id}-n`} aria-live="polite">
        {problem ? (
          <span className="text-danger">
            <Icon name="alertCircle" /> {problem}
          </span>
        ) : capturing ? (
          'Press a key…'
        ) : isDefault ? (
          ''
        ) : (
          `Default ${DEFAULT_SHORTCUTS[action].toUpperCase()}`
        )}
      </span>
      <button
        ref={btn}
        type="button"
        className={`keycap keycap-btn ${capturing ? 'is-capturing' : ''}`}
        aria-labelledby={`${id}-l`}
        aria-describedby={`${id}-n`}
        aria-pressed={capturing}
        aria-busy={saving || undefined}
        title={capturing ? 'Press the new key (Esc cancels, Delete restores the default)' : 'Change this key'}
        onClick={() => {
          setProblem('');
          setCapturing((c) => !c);
        }}
        onKeyDown={onKey}
      >
        {capturing ? '…' : key.toUpperCase()}
      </button>
    </li>
  );
}
