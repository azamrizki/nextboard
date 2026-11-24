// Where a connection is, in words: "MySQL 8.0 · This PC", "PostgreSQL 16 · db-01 over SSH".

import { useCallback } from 'react';
import { useMachines } from '../store';
import { driverOf, type Connection } from './types';

/** "MySQL 8.0 · This PC", "PostgreSQL 16 · db-01 over SSH": what the connection is and where. */
export function connWhere(c: Connection, version: string | undefined, machineName: (id: string) => string, full = false): string {
  const v = version && full ? version : version ? version.replace(/^(MySQL|MariaDB|PostgreSQL|Redis|Valkey)\s+(\d+(\.\d+)?).*$/, '$1 $2') : driverOf(c.driver).label;
  let where = /^(127\.|localhost$|::1$)/.test(c.host) ? 'This PC' : c.host;
  if (c.reach.kind === 'machine') where = machineName(c.reach.machineId ?? '') || 'a machine';
  if (c.reach.kind === 'ssh') where = `${c.reach.configHost || c.reach.hops?.[c.reach.hops.length - 1]?.host || 'SSH'} over SSH`;
  return `${v} · ${where}`;
}

export function useMachineName(): (id: string) => string {
  const machines = useMachines();
  return useCallback((id: string) => machines.find((m) => m.id === id)?.name ?? '', [machines]);
}

