// app.js — entry point. Initialises theme, mounts the new components.
// Legacy inline JS in index.html still drives the legacy cards visible below
// the new hero; we don't touch them yet (Phase 2-4).

import { initTheme } from './theme.js';
import { installExternalOpener } from './external.js';
import { mountShell } from './shell.js';
import { mountSidebar } from './sidebar.js';
import { mountLiveCharts } from './charts/live.js';
import { mountCommandPalette } from './command-palette.js';
import { mountRunDetail } from './run-detail.js';
import { mountReview } from './review.js';
import { mountClusterSidebar, mountClusterView, mountDistributeToggle, mountClusterIntercept } from './cluster-ui.js';
import { mountMasthead } from './masthead.js';
import { mountHostBar } from './host.js';
import { mountRunHeader } from './run-header.js';
import { mountHeroRun } from './runs.js';
import { mountConnectionCard } from './connection.js';
import { mountSourcesAndSinks } from './sources-sinks.js';
import { mountRecords } from './records.js';
import { mountRunsHistory } from './runs-history.js';
import { mountTrustedHosts } from './trusted-hosts.js';
import { mountUsersEditors } from './users-editor.js';
import { mountUploadRestructure } from './upload-restructure.js';
import { mountConfigureRedesign } from './configure-redesign.js';
import { mountRunActions } from './run-actions.js';
import { mountStartPreflight } from './start-preflight.js';
import { mountCeilingBanner } from './ceiling-banner.js';
import { mountWizard } from './wizard.js';
import { mountSavedConnections } from './saved-connections.js';

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
  mountSourcesAndSinks();
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
    // Redesign the Configure view AFTER upload-restructure + run-actions
    // so the run-mechanics group + Export/Import proxy buttons are in
    // place, and we can relocate them into the new section structure.
    mountConfigureRedesign();
    mountSavedConnections();
    mountStartPreflight();
    mountWizard('[data-component="wizard"]');
    mountCeilingBanner();
    // Palette mounts last — by now the shell's Cmd+K button exists
    // and legacy.js has set window.__sftplBuildRequestBody so saved-
    // config snapshots work.
    mountCommandPalette();
    mountRunDetail();
    mountReview();
    // Cluster UI mounts before sidebar so the workers section is in
    // place when sidebar fills its sections.
    mountClusterSidebar();
    mountClusterView();
    mountDistributeToggle();
    mountClusterIntercept();
    // Sidebar mounts AFTER palette so saved-configs reflects any
    // load that fired during palette init (rare, but tidy).
    mountSidebar();
  }, 0);
});
