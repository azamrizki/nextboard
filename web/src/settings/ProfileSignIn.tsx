// Signing in to a login profile from Settings (docs/API-agents.md §10): the hub starts the
// profile's CLI in a console of its own, without a board tile, and the CLI runs its own sign-in
// in the terminal shown here. Signed in = the CLI reached its prompt with that profile. The
// console is stopped when the dialog closes.

import { useEffect, useRef, useState } from 'react';
import { api, errorMessage } from '../api';
import { useTerminal } from '../board/useTerminal';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { useConsoleThemeAttr } from '../lib/theme';
import type { Profile } from '../lib/types';
import { useSession } from '../store';

/** Starts the sign-in console of a profile, and stops it when the component goes. */
function useSignInConsole(profileId: string) {
  const [sessionId, setSessionId] = useState('');
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let gone = false;
    let started = '';
    setError('');
    setSessionId('');
    api
      .profileSignIn(profileId)
      .then((s) => {
        started = s.id;
        if (gone) void api.deleteSession(s.id).catch(() => undefined);
        else setSessionId(s.id);
      })
      .catch((err) => {
        if (!gone) setError(errorMessage(err));
      });
    return () => {
      gone = true;
      if (started) void api.deleteSession(started).catch(() => undefined);
    };
  }, [profileId, attempt]);
  return { sessionId, error, retry: () => setAttempt((n) => n + 1) };
}

function SignInTerminal({ sessionId }: { sessionId: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const theme = useConsoleThemeAttr();
  useTerminal(sessionId, ref);
  return <div className="profile-signin-term" ref={ref} data-keep-escape="" data-theme={theme} data-session={sessionId} />;
}

/**
 * The terminal and what it says about the sign-in; signedIn turns true once the CLI is at its
 * prompt. agentName is the CLI in the terminal: for a NextCode profile, Claude Code.
 */
export function ProfileSignIn({ profile, agentName, onSignedIn }: { profile: Profile; agentName: string; onSignedIn?: (yes: boolean) => void }) {
  const { sessionId, error, retry } = useSignInConsole(profile.id);
  const s = useSession(sessionId);
  const state = s?.agentState;
  const signedIn = state === 'ready' || state === 'working';
  const stopped = !!s && (s.status === 'exited' || s.status === 'error' || state === 'exited' || state === 'error');
  useEffect(() => onSignedIn?.(signedIn), [signedIn, onSignedIn]);

  let note: { icon: 'check' | 'alertCircle' | 'info'; text: string; tone: string };
  if (error) note = { icon: 'alertCircle', text: `The sign-in could not start: ${error}`, tone: 'is-danger' };
  else if (stopped) note = { icon: 'alertCircle', text: `${agentName} stopped before it was signed in.`, tone: 'is-danger' };
  else if (signedIn) note = { icon: 'check', text: `Signed in: ${agentName} is at its prompt with “${profile.name}”.`, tone: 'is-success' };
  else if (!s || state === 'starting') note = { icon: 'info', text: `Starting ${agentName}…`, tone: '' };
  else note = { icon: 'info', text: `Follow ${agentName}’s sign-in in the terminal. Copy a link it prints into your browser if it does not open one.`, tone: '' };

  return (
    <div className="profile-signin">
      <div className="profile-signin-box">{sessionId ? <SignInTerminal sessionId={sessionId} /> : <div className="profile-signin-wait">{error ? '' : 'Starting…'}</div>}</div>
      <p className={`profile-signin-note ${note.tone}`} role="status">
        <Icon name={note.icon} />
        <span>{note.text}</span>
        {(error || stopped) && (
          <Button size="sm" variant="secondary" icon="refresh" onClick={retry}>
            Start again
          </Button>
        )}
      </p>
    </div>
  );
}

/**
 * Sign in to a profile: right after adding it (fresh), later from its row, or from a NextCode chat
 * whose login failed. cliName: the CLI that signs in when it is not the agent itself (a NextCode
 * profile signs in in Claude Code or Codex). onClose says whether it was signed in.
 */
export function ProfileSignInDialog({ profile, agentName, cliName, fresh, onClose: close }: { profile: Profile; agentName: string; cliName?: string; fresh?: boolean; onClose: (signedIn: boolean) => void }) {
  const [signedIn, setSignedIn] = useState(false);
  const onClose = () => close(signedIn);
  const cli = cliName || agentName;
  const other = cli !== agentName;
  return (
    <Modal
      title={other ? `Sign in to ${agentName} · “${profile.name}”` : `Sign in to “${profile.name}”`}
      icon="user"
      size="lg"
      onClose={onClose}
      footer={
        <Button variant={signedIn ? 'primary' : 'secondary'} onClick={onClose}>
          {signedIn ? 'Done' : fresh ? 'Sign in later' : 'Close'}
        </Button>
      }
    >
      <p className="setting-hint">
        {fresh ? `“${profile.name}” is added. ` : ''}
        {cli} runs here with this profile’s folder and asks for its login when it has none.{other ? ` ${agentName} then uses that login, and only ${agentName} does.` : ''} Closing stops this
        console; the login stays in the profile
        {fresh ? (other ? `; if you skip it now, sign in later from its row: ${agentName}’s chat cannot ask for a login.` : ', and a console started with the profile later asks again if you skip it now.') : '.'}
      </p>
      <ProfileSignIn profile={profile} agentName={cli} onSignedIn={setSignedIn} />
    </Modal>
  );
}
