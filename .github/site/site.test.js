import assert from 'node:assert/strict';
import test from 'node:test';

import { fallbackCopy, selectCommand } from './site.js';

test('fallbackCopy copies the full command passed to it', () => {
  const expected = "./bin/machinist run --command=foreman --repo=/path/to/repo --prompt='Complete issue #123'";
  let input;
  let removed = false;

  const fakeDocument = {
    body: {
      appendChild(element) {
        input = element;
      },
    },
    createElement() {
      return {
        value: '',
        style: {},
        setAttribute() {},
        select() {},
        remove() { removed = true; },
      };
    },
    execCommand(command) {
      assert.equal(command, 'copy');
      assert.equal(input.value, expected);
      return true;
    },
  };

  assert.equal(fallbackCopy(expected, fakeDocument), true);
  assert.equal(removed, true);
});

test('fallbackCopy removes its temporary input when copying throws', () => {
  let removed = false;
  const fakeDocument = {
    body: { appendChild() {} },
    createElement() {
      return { value: '', style: {}, setAttribute() {}, select() {}, remove() { removed = true; } };
    },
    execCommand() { throw new Error('copy unavailable'); },
  };

  assert.throws(() => fallbackCopy('command', fakeDocument), /copy unavailable/);
  assert.equal(removed, true);
});

test('selectCommand exposes and selects the full data-copy value', () => {
  const code = { textContent: 'machinist run ...' };
  const button = { dataset: { copy: 'machinist run --command=foreman --repo=/path/to/repo' }, querySelector: () => code };
  let selectedNode;
  const range = { selectNodeContents(node) { selectedNode = node; } };
  const fakeDocument = { createRange: () => range };
  const ranges = [];
  const selection = { removeAllRanges() { ranges.length = 0; }, addRange(value) { ranges.push(value); } };

  selectCommand(button, fakeDocument, selection);

  assert.equal(code.textContent, button.dataset.copy);
  assert.equal(selectedNode, code);
  assert.deepEqual(ranges, [range]);
});

test('setup snippets include required directories and Codex permissions', async () => {
  const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./index.html', import.meta.url), 'utf8'));
  assert.match(source, /mkdir -p \.\/bin &amp;&amp; go build/);
  assert.match(source, /"--sandbox"[\s\S]+"danger-full-access"/);
  assert.doesNotMatch(source, /pipeline|--agent/);
});

test('public identity uses the fitted SVG mark and current positioning', async () => {
  const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./index.html', import.meta.url), 'utf8'));
  const styles = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./styles.css', import.meta.url), 'utf8'));

  assert.match(source, /machinist-mark\.svg/);
  assert.match(source, /A better way to put coding agents to work\./);
  assert.doesNotMatch(source, /machinist-mark\.png|fonts\.googleapis\.com/);
  assert.match(styles, /--mark: #66a9c2/);
  assert.doesNotMatch(styles, /#f08b24|#f2ede3|DM Mono|Inter Tight/);
});

test('repository quick start links to existing guides and stays directly runnable', async () => {
  const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('../../README.md', import.meta.url), 'utf8'));

  for (const guide of ['docs/README.md', 'docs/configuration.md', 'docs/development.md', 'docs/vm-deployment.md']) {
    assert.match(source, new RegExp(guide.replace('.', '\\.')));
  }
  assert.doesNotMatch(source, /machinist submit|docs\/getting-started|docs\/how-it-works|docs\/control-plane|docs\/secure-self-hosting/);
});
