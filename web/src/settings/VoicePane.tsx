// Settings › Voice (D11–D13, docs/API-studio.md §11): per browser, opt-in with the
// disclosure that the browser's recogniser may send audio to Google or Microsoft.

import { useEffect, useState } from 'react';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { SettingRow, SwitchRow } from '../components/Switch';
import { Icon } from '../components/Icon';
import { DICTATION_KEYS, setVoicePrefs, speak, speechSupported, ttsSupported, useVoicePrefs } from '../voice/voice';

const LANGS = [
  ['en-US', 'English (US)'],
  ['en-GB', 'English (UK)'],
  ['id-ID', 'Bahasa Indonesia'],
  ['de-DE', 'Deutsch'],
  ['es-ES', 'Español'],
  ['fr-FR', 'Français'],
  ['it-IT', 'Italiano'],
  ['nl-NL', 'Nederlands'],
  ['pt-BR', 'Português (Brasil)'],
  ['ja-JP', '日本語'],
] as const;

export const VOICE_DISCLOSURE =
  'Speech recognition here is provided by your browser. In Chrome and Edge your audio is sent to Google or Microsoft to be transcribed. NextBoard never stores or sends audio. Turn on voice?';

export function VoicePane() {
  const p = useVoicePrefs();
  const [ask, setAsk] = useState(false);
  const [voices, setVoices] = useState<SpeechSynthesisVoice[]>([]);
  const supported = speechSupported();

  useEffect(() => {
    if (!ttsSupported()) return;
    const load = () => setVoices(window.speechSynthesis.getVoices());
    load();
    window.speechSynthesis.addEventListener?.('voiceschanged', load);
    return () => window.speechSynthesis.removeEventListener?.('voiceschanged', load);
  }, []);

  if (!supported)
    return (
      <div className="settings-cards">
        <section className="card settings-card">
          <p className="setting-hint">
            <Icon name="micOff" /> This browser has no speech recognition, so voice controls stay hidden here. Chrome and Edge have it.
          </p>
        </section>
      </div>
    );

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="vc-main">
        <h3 id="vc-main" className="section-title">
          Voice (this browser)
        </h3>
        <SwitchRow
          title="Use voice"
          hint="Hold Space to give an order, hold a key to dictate. Your browser recognises the speech; the hub never receives audio."
          checked={p.enabled}
          onChange={(v) => (v ? setAsk(true) : setVoicePrefs({ enabled: false }))}
        />
        <p className="setting-hint">{VOICE_DISCLOSURE.replace(' Turn on voice?', '')}</p>
        <SettingRow title="Language" htmlFor="vc-lang">
          <select id="vc-lang" className="control control-sm" value={p.lang} onChange={(e) => setVoicePrefs({ lang: e.target.value })}>
            {LANGS.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </SettingRow>
        <SwitchRow title="Hold Space to talk" hint="On the board, when no console or field has the keyboard. Release sends; Esc cancels; recordings over 60 seconds are not sent." checked={p.holdSpace} disabled={!p.enabled} onChange={(v) => setVoicePrefs({ holdSpace: v })} />
        <SettingRow title="Hold to dictate" hint="Types into the focused field or console without pressing Enter. Say “comma”, “period”, “question mark”, “new line”." htmlFor="vc-key">
          <select id="vc-key" className="control control-sm" value={p.dictationKey} disabled={!p.enabled} onChange={(e) => setVoicePrefs({ dictationKey: e.target.value })}>
            {DICTATION_KEYS.map((k) => (
              <option key={k.code} value={k.code}>
                {k.label}
              </option>
            ))}
          </select>
        </SettingRow>
      </section>
      {ttsSupported() && (
        <section className="card settings-card" aria-labelledby="vc-tts">
          <h3 id="vc-tts" className="section-title">
            Spoken replies
          </h3>
          <SwitchRow
            title="Confirm out loud"
            hint="Short lines only: a question about your order, who finished, who needs you. Agent replies are never read out. Uses this device’s voices."
            checked={p.ttsConfirmations}
            disabled={!p.enabled}
            onChange={(v) => setVoicePrefs({ ttsConfirmations: v })}
          />
          <SettingRow title="Voice" htmlFor="vc-voice">
            <div className="inline-controls">
              <select id="vc-voice" className="control control-sm vc-voice" value={p.ttsVoice} disabled={!p.enabled} onChange={(e) => setVoicePrefs({ ttsVoice: e.target.value })}>
                <option value="">Default</option>
                {voices
                  .filter((v) => v.lang.slice(0, 2) === p.lang.slice(0, 2))
                  .map((v) => (
                    <option key={v.voiceURI} value={v.voiceURI}>
                      {v.name}
                    </option>
                  ))}
              </select>
              <Button size="sm" variant="ghost" icon="volume" disabled={!p.enabled || !p.ttsConfirmations} onClick={() => speak('Arkan finished.')}>
                Test
              </Button>
            </div>
          </SettingRow>
        </section>
      )}
      {ask && (
        <ConfirmDialog title="Turn on voice?" icon="mic" confirmLabel="Turn on voice" onClose={() => setAsk(false)} onConfirm={() => setVoicePrefs({ enabled: true })}>
          <p>{VOICE_DISCLOSURE.replace(' Turn on voice?', '')}</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
