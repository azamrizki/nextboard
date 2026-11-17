// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import { closesOn } from './Popover';

describe('what closes a popover', () => {
  const html = `<button id="trigger">New console</button>
    <div class="popover" id="pop"><span id="inside">Resume a chat</span></div>
    <div class="modal-layer"><div id="dialog"><p id="line">Transcript</p></div></div>
    <ul class="menu-list"><li id="item">Rename…</li></ul>
    <main id="board"><p id="away">board</p></main>`;
  document.body.innerHTML = html;
  const $ = (id: string) => document.getElementById(id)!;
  const pop = $('pop');
  const trigger = $('trigger');

  it('a press or a wheel on the board does', () => {
    expect(closesOn($('away'), pop, trigger)).toBe(true);
    expect(closesOn($('away').firstChild, pop, null)).toBe(true);
  });

  it('one in the popover or on its trigger does not', () => {
    expect(closesOn($('inside'), pop, trigger)).toBe(false);
    expect(closesOn(trigger, pop, trigger)).toBe(false);
  });

  it('scrolling a dialog or a menu opened from it does not', () => {
    // the Transcript dialog of the New console form's "Resume a chat" tab
    expect(closesOn($('line'), pop, null)).toBe(false);
    expect(closesOn($('line').firstChild, pop, null)).toBe(false);
    expect(closesOn($('item'), pop, null)).toBe(false);
  });
});
