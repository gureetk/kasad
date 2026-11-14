// What the page does beyond plain HTML: the theme switch, the install tabs,
// the copy buttons and the protocol demonstration. Without this script the
// page still reads in full: every install method is shown, and the demo shows
// its first command. theme.js has already put a stored theme in place.

import { INITIAL_KEY, encrypt, hex, lengthPrefix } from './protocol.js';

const STORAGE_KEY = 'kasad-theme';
const BACKGROUNDS = { light: '#EEF3EE', dark: '#111814' };

function storedChoice() {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    return value === 'light' || value === 'dark' ? value : 'system';
  } catch {
    return 'system';
  }
}

// applyChoice puts a theme choice in place and remembers it. "system" means
// following the device, which CSS does by itself when no theme is set.
function applyChoice(choice) {
  const root = document.documentElement;
  if (choice === 'system') {
    root.removeAttribute('data-theme');
  } else {
    root.setAttribute('data-theme', choice);
  }

  // The colour browsers give their own bars. Each tag is for one system
  // setting; a choice made on the page applies to both.
  for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
    const own = (meta.getAttribute('media') || '').includes('dark') ? 'dark' : 'light';
    meta.setAttribute('content', BACKGROUNDS[choice === 'system' ? own : choice]);
  }

  try {
    if (choice === 'system') {
      localStorage.removeItem(STORAGE_KEY);
    } else {
      localStorage.setItem(STORAGE_KEY, choice);
    }
  } catch {
    // Without storage the choice lasts until the page is left.
  }
}

function setUpThemeSwitch() {
  const group = document.querySelector('[data-theme-switch]');
  if (!group) {
    return;
  }
  const buttons = [...group.querySelectorAll('button[data-choice]')];
  const mark = (choice) => {
    for (const button of buttons) {
      button.setAttribute('aria-pressed', String(button.dataset.choice === choice));
    }
  };
  mark(storedChoice());
  for (const button of buttons) {
    button.addEventListener('click', () => {
      applyChoice(button.dataset.choice);
      mark(button.dataset.choice);
    });
  }
}

function setUpTabs() {
  const group = document.querySelector('[data-tabs]');
  if (!group) {
    return;
  }
  const buttons = [...group.querySelectorAll('button[data-tab]')];
  const panels = [...document.querySelectorAll('[data-panel]')];
  for (const button of buttons) {
    button.addEventListener('click', () => {
      for (const other of buttons) {
        other.setAttribute('aria-pressed', String(other === button));
      }
      for (const panel of panels) {
        panel.classList.toggle('is-active', panel.dataset.panel === button.dataset.tab);
      }
    });
  }
}

function setUpCopyButtons() {
  const status = document.getElementById('copy-status');
  for (const button of document.querySelectorAll('[data-copy]')) {
    let timer = 0;
    button.addEventListener('click', async () => {
      const code = button.closest('.code')?.querySelector('code');
      if (!code) {
        return;
      }
      clearTimeout(timer);
      try {
        await navigator.clipboard.writeText(code.textContent);
        button.classList.add('is-copied');
        status.textContent = 'Copied.';
      } catch {
        status.textContent = 'Could not copy. Select the commands instead.';
      }
      timer = setTimeout(() => {
        button.classList.remove('is-copied');
        status.textContent = '';
      }, 2000);
    });
  }
}

function setUpDemo() {
  const demo = document.querySelector('[data-demo]');
  if (!demo) {
    return;
  }
  const out = (name) => demo.querySelector(`[data-out="${name}"]`);
  const chips = [...demo.querySelectorAll('button[data-json]')];
  const steps = [...demo.querySelectorAll('[data-step]')];

  const show = (chip) => {
    const json = chip.dataset.json;
    const plain = new TextEncoder().encode(json);
    const wire = encrypt(json);
    out('json').textContent = json;
    out('length').textContent = hex(lengthPrefix(wire.length));
    out('count').textContent = `${wire.length} bytes of JSON follow`;
    out('bytes').textContent = hex(wire);
    steps.forEach((step, i) => {
      const key = i === 0 ? INITIAL_KEY : wire[i - 1];
      step.querySelector('[data-part="char"]').textContent = json.charAt(i);
      step.querySelector('[data-part="in"]').textContent = `0x${hex([plain[i]])}`;
      step.querySelector('[data-part="key"]').textContent = `0x${hex([key])}`;
      step.querySelector('[data-part="out"]').textContent = `0x${hex([wire[i]])}`;
    });
    for (const other of chips) {
      other.setAttribute('aria-pressed', String(other === chip));
    }
  };
  for (const chip of chips) {
    chip.addEventListener('click', () => show(chip));
  }
}

setUpThemeSwitch();
setUpTabs();
setUpCopyButtons();
setUpDemo();
