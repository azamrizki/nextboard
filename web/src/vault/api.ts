// The vault's REST API (docs/API-vault.md). Only ciphertext passes through it.

import { apiRequest } from '../api';
import type { KeyMaterial } from './crypto';

export interface VaultHeader extends KeyMaterial {
  id: string;
  keyRev: number;
  createdAt: string;
  updatedAt: string;
}

export interface VaultBlob {
  id: string;
  blob: string;
  rev: number;
  updatedAt: string;
}

export interface VaultView {
  exists: boolean;
  header?: VaultHeader;
  items: VaultBlob[] | null;
  rev: number;
}

const enc = encodeURIComponent;

export const vaultApi = {
  get: () => apiRequest<VaultView>('GET', '/api/vault'),
  init: (m: KeyMaterial) => apiRequest<VaultHeader>('POST', '/api/vault', m),
  setKey: (m: KeyMaterial, baseKeyRev: number) => apiRequest<VaultHeader>('PUT', '/api/vault/key', { ...m, baseKeyRev }),
  put: (id: string, blob: string, baseRev: number) => apiRequest<VaultBlob>('PUT', `/api/vault/items/${enc(id)}`, { blob, baseRev }),
  remove: (id: string, baseRev: number) => apiRequest<{ ok: boolean }>('DELETE', `/api/vault/items/${enc(id)}?baseRev=${baseRev}`),
  batch: (items: { id: string; blob: string }[]) => apiRequest<{ items: VaultBlob[] }>('POST', '/api/vault/batch', { items }),
  pinCreate: (proof: string) => apiRequest<{ id: string; secret: string }>('POST', '/api/vault/pins', { proof }),
  pinOpen: (id: string, proof: string) => apiRequest<{ secret: string }>('POST', `/api/vault/pins/${enc(id)}/open`, { proof }),
  pinRemove: (id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/vault/pins/${enc(id)}`),
  pinsClear: () => apiRequest<{ ok: boolean }>('DELETE', '/api/vault/pins'),
  reset: () => apiRequest<{ ok: boolean; kept: string }>('POST', '/api/vault/reset', { confirm: 'RESET' }),
};
