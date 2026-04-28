// app.js — entry point. Initialises theme, mounts the new components.
// Legacy inline JS in index.html still drives the legacy cards visible below
// the new hero; we don't touch them yet (Phase 2-4).

import { initTheme } from './theme.js';
import { mountMasthead } from './masthead.js';
import { mountHostBar } from './host.js';
import { mountHeroRun } from './runs.js';
import { mountConnectionCard } from './connection.js';

initTheme();

document.addEventListener('DOMContentLoaded', () => {
  mountMasthead('[data-component="masthead"]');
  mountHostBar('[data-component="host-bar"]');
  mountHeroRun('[data-component="hero-run"]');
  mountConnectionCard('[data-component="connection"]');
});
