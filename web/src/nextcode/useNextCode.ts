// One NextCode console's chat, live: the socket's frames (a snapshot first, then each change)
// through the reducer, and the console's actions over REST. The socket reconnects with backoff
// (lib/socket.ts); a view that fell behind is closed with 1013 and takes a new snapshot when it
// is back. While the socket cannot open, GET tells why and still shows the chat.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage, isCode, isStatus } from '../api';
import { toast } from '../components/Toast';
import { ReconnectingSocket, type SocketState } from '../lib/socket';
import { chatSocketPath, nextcodeApi, type ModelChange } from './api';
import { modelErrorText } from './modelMenu';
import { emptyChat, reduceAll, type ChatAction, type ChatState } from './reducer';
import type { Answer, Frame, PermissionMode } from './types';

export interface NextCodeChatController {
  sessionId: string;
  state: ChatState;
  /** The chat's socket. */
  conn: SocketState;
  /** Why the chat cannot be read now (the socket is down and GET failed), else "". */
  problem: string;
  loadingOlder: boolean;
  /** Resolves true when the hub took the message. */
  send: (text: string) => Promise<boolean>;
  answer: (requestId: string, a: Answer) => Promise<boolean>;
  interrupt: () => Promise<void>;
  setMode: (mode: PermissionMode) => Promise<void>;
  /** Resolves true when the hub took it; the console's model and effort follow in its session events. */
  setModel: (c: ModelChange) => Promise<boolean>;
  loadOlder: () => Promise<void>;
}


/** Older items asked for at once. */
const PAGE = 300;

export function useNextCode(sessionId: string): NextCodeChatController {
  const [state, setState] = useState<ChatState>(emptyChat);
  const [conn, setConn] = useState<SocketState>('connecting');
  const [problem, setProblem] = useState('');
  const [loadingOlder, setLoadingOlder] = useState(false);
  const stateRef = useRef(state);
  stateRef.current = state;

  // frames that came in one animation frame are applied together: a reply streams token by token
  const queue = useRef<ChatAction[]>([]);
  const timer = useRef<{ id: number; frame: boolean } | null>(null);
  const dispatch = useCallback((a: ChatAction) => {
    queue.current.push(a);
    if (timer.current) return;
    const flush = () => {
      timer.current = null;
      const list = queue.current;
      queue.current = [];
      if (list.length) setState((s) => reduceAll(s, list));
    };
    // a hidden tab gets no animation frames: a timer still applies them (slowly)
    timer.current = document.hidden ? { id: window.setTimeout(flush, 100), frame: false } : { id: window.requestAnimationFrame(flush), frame: true };
  }, []);

  useEffect(() => {
    setState(emptyChat);
    setProblem('');
    queue.current = [];
    let alive = true;
    let snapshot = false;
    // GET while the socket is down: the chat as it is, or why it cannot be read
    let asking = false;
    const ask = () => {
      if (asking) return;
      asking = true;
      nextcodeApi
        .page(sessionId)
        .then((p) => {
          if (!alive || snapshot) return;
          dispatch({ t: 'snapshot', items: p.items, more: p.more, requests: p.requests, status: p.status });
          setProblem('');
        })
        .catch((err) => {
          if (!alive) return;
          if (isCode(err, 'not_nextcode')) setProblem('This console is not a NextCode console.');
          else if (isCode(err, 'nextcode_not_running')) setProblem('The chat is not open yet: the console is starting, or it failed to start.');
          else if (isStatus(err, 404)) setProblem('This console is gone.');
          else setProblem(`Could not read the chat: ${errorMessage(err)}`);
        })
        .finally(() => {
          asking = false;
        });
    };
    const sock = new ReconnectingSocket({
      path: chatSocketPath(sessionId),
      onStateChange: (s) => alive && setConn(s),
      onOpen: () => {
        snapshot = false;
      },
      onMessage: (ev) => {
        if (typeof ev.data !== 'string') return;
        let f: Frame;
        try {
          f = JSON.parse(ev.data) as Frame;
        } catch {
          return;
        }
        if (!f || typeof f.t !== 'string') return;
        if (f.t === 'snapshot') {
          snapshot = true;
          setProblem('');
        }
        dispatch(f);
      },
      onClose: () => {
        // never opened, or closed before its snapshot: ask the hub over REST
        if (alive && !snapshot) ask();
        snapshot = false;
      },
    });
    return () => {
      alive = false;
      sock.close();
      const t = timer.current;
      if (t) {
        if (t.frame) window.cancelAnimationFrame(t.id);
        else window.clearTimeout(t.id);
        timer.current = null;
      }
      queue.current = [];
    };
  }, [sessionId, dispatch]);

  const send = useCallback(
    async (text: string) => {
      try {
        await nextcodeApi.send(sessionId, text);
        return true;
      } catch (err) {
        if (isCode(err, 'too_long')) toast.error('That message is too long: keep it under 100,000 characters.');
        else if (isCode(err, 'cli_failed')) toast.error(`NextCode could not start: ${errorMessage(err)}`);
        else toast.error(`The message was not sent: ${errorMessage(err)}`);
        return false;
      }
    },
    [sessionId],
  );

  const answer = useCallback(
    async (requestId: string, a: Answer) => {
      try {
        await nextcodeApi.answer(sessionId, requestId, a);
        dispatch({ t: 'resolved', id: requestId });
        return true;
      } catch (err) {
        if (isCode(err, 'request_gone')) {
          dispatch({ t: 'resolved', id: requestId });
          toast.info('That request was already answered, or the agent no longer waits for it.');
          return true;
        }
        toast.error(`The answer was not sent: ${errorMessage(err)}`);
        return false;
      }
    },
    [sessionId, dispatch],
  );

  const interrupt = useCallback(async () => {
    try {
      await nextcodeApi.interrupt(sessionId);
    } catch (err) {
      // the turn ended in the meantime
      if (!isCode(err, 'not_working')) toast.error(`Could not stop it: ${errorMessage(err)}`);
    }
  }, [sessionId]);

  const setMode = useCallback(
    async (mode: PermissionMode) => {
      try {
        const r = await nextcodeApi.setMode(sessionId, mode);
        if (r?.status) dispatch({ t: 'status', status: r.status });
      } catch (err) {
        toast.error(`Could not change the permission mode: ${errorMessage(err)}`);
      }
    },
    [sessionId, dispatch],
  );

  const setModel = useCallback(
    async (c: ModelChange) => {
      try {
        const r = await nextcodeApi.setModel(sessionId, c);
        if (r?.status) dispatch({ t: 'status', status: r.status });
        return true;
      } catch (err) {
        toast.error(modelErrorText(err));
        return false;
      }
    },
    [sessionId, dispatch],
  );

  const loadOlder = useCallback(async () => {
    const first = stateRef.current.items[0];
    if (!first) return;
    setLoadingOlder(true);
    try {
      const p = await nextcodeApi.page(sessionId, first.id, PAGE);
      dispatch({ t: 'older', items: p.items ?? [], more: p.more });
    } catch (err) {
      toast.error(`Could not load earlier messages: ${errorMessage(err)}`);
    } finally {
      setLoadingOlder(false);
    }
  }, [sessionId, dispatch]);

  return useMemo(
    () => ({ sessionId, state, conn, problem, loadingOlder, send, answer, interrupt, setMode, setModel, loadOlder }),
    [sessionId, state, conn, problem, loadingOlder, send, answer, interrupt, setMode, setModel, loadOlder],
  );
}
