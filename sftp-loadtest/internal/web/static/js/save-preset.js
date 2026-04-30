// save-preset.js — modal-driven prompt to save the current form as a
// named preset. Used by the Configure prelude's Save preset… button
// and (replacing the legacy window.prompt path) by the Cmd+K palette.
//
// Wails desktop blocks window.prompt outright — the only reliable
// cross-SKU path is the in-DOM modal in modal.js.

import { form as formModal } from './modal.js';
import { save as saveConfig, list as listConfigs } from './saved-configs.js';
import { pushToast } from './toast.js';

export async function promptSavePreset() {
  const existing = listConfigs().map((c) => c.name);
  const suggestion = nextSuggestedName(existing);
  const out = await formModal({
    title: 'Save preset',
    submitLabel: 'Save',
    fields: [
      {
        name: 'name',
        label: 'Preset name',
        placeholder: suggestion,
        value: '',
        required: true,
        hint: existing.length
          ? 'Reuse an existing name to overwrite that preset; type a new name to create a fresh one.'
          : 'A friendly label that shows up in the sidebar and the ⌘K palette.',
      },
    ],
  });
  if (!out) return null;
  const name = (out.name || '').trim() || suggestion;
  const entry = saveConfig(name);
  if (entry) {
    pushToast(`Saved preset “${entry.name}”`, 'success');
  } else {
    pushToast('Could not save preset (storage full?)', 'error');
  }
  return entry;
}

function nextSuggestedName(taken) {
  const base = 'preset';
  const set = new Set(taken);
  for (let i = 1; i < 100; i++) {
    const cand = `${base}-${i}`;
    if (!set.has(cand)) return cand;
  }
  return `${base}-${Date.now().toString(36)}`;
}
