// app.js — entry point. Initialises theme, mounts the new components.
// Legacy inline JS in index.html still drives the legacy cards visible below
// the new hero; we don't touch them yet (Phase 2-4).

import { initTheme } from './theme.js';
import { installExternalOpener } from './external.js';
import { mountShell } from './shell.js';
import { mountLiveCharts } from './charts/live.js';
import { mountMasthead } from './masthead.js';
import { mountHostBar } from './host.js';
import { mountRunHeader } from './run-header.js';
import { mountHeroRun } from './runs.js';
import { mountConnectionCard } from './connection.js';
import { mountRecords } from './records.js';
import { mountRunsHistory } from './runs-history.js';
import { mountTrustedHosts } from './trusted-hosts.js';
import { mountUsersEditors } from './users-editor.js';
import { mountUploadRestructure } from './upload-restructure.js';
import { mountRunActions } from './run-actions.js';
import { mountStartPreflight } from './start-preflight.js';
import { mountCeilingBanner } from './ceiling-banner.js';
import { mountWizard } from './wizard.js';

initTheme();
installExternalOpener();

document.addEventListener('DOMContentLoaded', () => {
  // Wrap existing markup in the shell first so theme + mounts can find
  // the topbar's elements where they belong.
  mountShell();
  mountMasthead('[data-component="masthead"]');
  mountRunHeader('[data-component="run-header"]');
  mountHostBar('[data-component="host-bar"]');
  mountHeroRun('[data-component="hero-run"]');
  mountConnectionCard('[data-component="connection"]');
  mountRecords('[data-component="records"]');
  // Real-time charts mount inside the records panel — must come AFTER
  // mountRecords so the panel exists.
  mountLiveCharts('[data-component="records"]');
  mountRunsHistory('[data-component="runs-history"]');
  mountTrustedHosts('[data-component="trusted-hosts"]');
  // Order matters: upload-restructure relocates DOM, users-editors then
  // mounts on the textareas in their final positions, wizard tags cards
  // last so it sees the merged structure.
  setTimeout(() => {
    mountUploadRestructure();
    mountUsersEditors();
    mountRunActions();
    mountStartPreflight();
    mountWizard('[data-component="wizard"]');
    mountCeilingBanner();
  }, 0);
});
