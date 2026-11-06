// A studio's list and what is picked from it (DESIGN.md §3.6): side by side on a wide screen,
// stacked under 900 px, and one at a time on a phone, where picking shows it and a back button
// returns to the list. The classes only matter on a phone (studio.css).

import { useState } from 'react';
import { Icon } from '../components/Icon';

/** `onMain`: opened for one item (a note from a file card), which shows first. */
export function useStudioPane(listLabel: string, onMain = false) {
  const [main, setMain] = useState(onMain);
  return {
    splitClass: main ? 'shows-main' : 'shows-side',
    showMain: () => setMain(true),
    back: (
      <button type="button" className="studio-back" onClick={() => setMain(false)}>
        <Icon name="chevronLeft" />
        {listLabel}
      </button>
    ),
  };
}
