// TextMate grammars for Monaco: vscode-textmate with the Oniguruma regex engine as WASM,
// loaded only when an installed extension brings a grammar.

import { createOnigScanner, createOnigString, loadWASM } from 'vscode-oniguruma';
import onigUrl from 'vscode-oniguruma/release/onig.wasm?url';
import { INITIAL, parseRawGrammar, Registry, type IGrammar, type StateStack } from 'vscode-textmate';
import type { monaco } from './monaco';

let onig: Promise<void> | null = null;
const ONIG_LOAD_MS = 20_000;
// a limit, so that an engine that fails to start cannot hold up whatever waits for it
const loadOnig = () =>
  (onig ??= Promise.race([
    fetch(onigUrl).then(async (r) => loadWASM(await r.arrayBuffer())),
    new Promise<void>((_, reject) => window.setTimeout(() => reject(new Error('The grammar engine did not start.')), ONIG_LOAD_MS)),
  ]));

type Source = (scope: string) => Promise<{ text: string; path: string } | null>;

class TmState implements monaco.languages.IState {
  constructor(readonly stack: StateStack) {}
  clone() {
    return new TmState(this.stack);
  }
  equals(other: monaco.languages.IState) {
    return other instanceof TmState && other.stack === this.stack;
  }
}

export async function tokensProviderFor(scopeName: string, source: Source): Promise<monaco.languages.TokensProvider | null> {
  await loadOnig();
  const registry = new Registry({
    onigLib: Promise.resolve({ createOnigScanner: (p: string[]) => createOnigScanner(p), createOnigString: (s: string) => createOnigString(s) }),
    loadGrammar: async (scope) => {
      const hit = await source(scope);
      return hit ? parseRawGrammar(hit.text, hit.path) : null;
    },
  });
  const grammar: IGrammar | null = await registry.loadGrammar(scopeName);
  if (!grammar) return null;
  return {
    getInitialState: () => new TmState(INITIAL),
    tokenize(line, state) {
      const r = grammar.tokenizeLine(line, (state as TmState).stack);
      return {
        endState: new TmState(r.ruleStack),
        // the deepest scope: theme rules match it by prefix ("string" for "string.quoted.double")
        tokens: r.tokens.map((t) => ({ startIndex: t.startIndex, scopes: t.scopes[t.scopes.length - 1] ?? '' })),
      };
    },
  };
}
