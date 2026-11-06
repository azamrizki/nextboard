// Keys a phone's keyboard does not have, for the console that has the keyboard (or fills the
// screen) on a touch screen (DESIGN.md §3.9): Esc, Tab, Shift+Tab, one-shot Ctrl and Alt, the
// arrows, ^C, Enter and a few symbols, then copy, attach and the keyboard itself. Pressing a key
// does not take the focus, so the on-screen keyboard stays up.

import { useEffect, useRef, useState, type ReactNode, type SyntheticEvent } from 'react';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { errorMessage } from '../api';
import { copyText } from '../lib/clipboard';
import { applyMods, keySequence, liveConsole, toggleMod, useMods, type KeyName } from './consoleKeys';

const keep = (e: SyntheticEvent) => e.preventDefault();

export function Key({ label, icon, name, pressed, onPress }: { label?: ReactNode; icon?: IconName; name: string; pressed?: boolean; onPress: () => void }) {
  return (
    <button type="button" className="key" aria-label={name} title={name} aria-pressed={pressed} onPointerDown={keep} onMouseDown={keep} onClick={onPress}>
      {icon ? <Icon name={icon} /> : label}
    </button>
  );
}

const MAX_LINES = 2000;

function CopyText({ sessionId, name, onClose }: { sessionId: string; name: string; onClose: () => void }) {
  const [text] = useState(() => liveConsole(sessionId)?.text(MAX_LINES) ?? '');
  const pre = useRef<HTMLPreElement>(null);
  // the end is what was just said
  useEffect(() => {
    if (pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, []);
  return (
    <Modal
      title={`Copy text from ${name}`}
      icon="copy"
      size="lg"
      onClose={onClose}
      help="Select with your finger, or copy everything."
      footer={
        <Button
          variant="primary"
          icon="copy"
          disabled={!text}
          onClick={() =>
            void copyText(text).then((ok) => {
              if (ok) {
                toast.success('Copied.');
                onClose();
              } else toast.error('This browser would not copy. Select the text and copy it instead.');
            })
          }
        >
          Copy all
        </Button>
      }
    >
      <pre ref={pre} className="copy-text" tabIndex={0}>
        {text || 'Nothing on the screen yet.'}
      </pre>
    </Modal>
  );
}

export function KeyBar({ sessionId, name }: { sessionId: string; name: string }) {
  const mods = useMods();
  const [copying, setCopying] = useState(false);
  const file = useRef<HTMLInputElement>(null);

  // the focused card's area and the bar above the keyboard leave room for this
  useEffect(() => {
    document.documentElement.setAttribute('data-keybar', '');
    return () => document.documentElement.removeAttribute('data-keybar');
  }, []);

  const send = (k: KeyName) => {
    const c = liveConsole(sessionId);
    if (c) c.send(keySequence(k, c.appCursor()));
  };
  const type = (t: string) => liveConsole(sessionId)?.send(applyMods(t));
  const keyboard = () => {
    const a = document.activeElement as HTMLElement | null;
    if (a?.closest('.xterm')) a.blur();
    else liveConsole(sessionId)?.focus();
  };

  return (
    <div className="key-bar" role="toolbar" aria-label={`Keys for ${name}`}>
      <div className="key-bar-keys">
        {/* what answering an agent's prompt takes comes first: a phone shows about six */}
        <Key label="Esc" name="Esc" onPress={() => send('esc')} />
        <Key icon="arrowUp" name="Up" onPress={() => send('up')} />
        <Key icon="arrowDown" name="Down" onPress={() => send('down')} />
        <Key label="⏎" name="Enter" onPress={() => send('enter')} />
        <Key label="Tab" name="Tab" onPress={() => send('tab')} />
        <Key label="Ctrl" name="Ctrl: changes the next key" pressed={mods.ctrl} onPress={() => toggleMod('ctrl')} />
        <Key label="^C" name="Ctrl+C: stop" onPress={() => send('ctrl-c')} />
        <Key icon="arrowLeft" name="Left" onPress={() => send('left')} />
        <Key icon="arrowRight" name="Right" onPress={() => send('right')} />
        <Key label="⇧Tab" name="Shift+Tab" onPress={() => send('backtab')} />
        <Key label="Alt" name="Alt: changes the next key" pressed={mods.alt} onPress={() => toggleMod('alt')} />
        {['/', '|', '~', '-'].map((t) => (
          <Key key={t} label={t} name={t} onPress={() => type(t)} />
        ))}
      </div>
      <div className="key-bar-tools">
        <Key icon="copy" name="Copy text" onPress={() => setCopying(true)} />
        <Key icon="upload" name="Attach a file or photo" onPress={() => file.current?.click()} />
        <Key icon="keyboard" name="Show or hide the keyboard" onPress={keyboard} />
      </div>
      <input
        ref={file}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = '';
          if (files.length) void liveConsole(sessionId)?.attach(files).catch((err) => toast.error(`Could not attach: ${errorMessage(err)}`));
        }}
      />
      {copying && <CopyText sessionId={sessionId} name={name} onClose={() => setCopying(false)} />}
    </div>
  );
}
