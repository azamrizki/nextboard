// Who is logged in (admin via cookie, or a paired device via a bearer-injecting proxy)
// and hub info. Provided by App once the boot flow has authenticated.

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { api } from './api';
import type { Hub, Me } from './lib/types';

export interface AuthInfo {
  me: Me;
  isAdmin: boolean;
  hub: Hub | null;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthInfo | null>(null);

interface AuthProviderProps {
  me: Me;
  onLogout: () => Promise<void>;
  children: ReactNode;
}

export function AuthProvider({ me, onLogout, children }: AuthProviderProps) {
  const [hub, setHub] = useState<Hub | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .hub()
      .then((h) => alive && setHub(h))
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  const value: AuthInfo = { me, isAdmin: me.kind === 'admin', hub, logout: onLogout };
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthInfo {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth outside AuthProvider');
  return ctx;
}
