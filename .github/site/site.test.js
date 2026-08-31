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

test('public identity uses the two-piece interlocking mark and current positioning', async () => {
  const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./index.html', import.meta.url), 'utf8'));
  const styles = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./styles.css', import.meta.url), 'utf8'));
  const mark = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./machinist-mark.svg', import.meta.url), 'utf8'));
  const socialCard = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('./social-card.svg', import.meta.url), 'utf8'));

  assert.match(source, /machinist-mark\.svg/);
  assert.match(source, /technical-drawings\.webp/);
  assert.match(source, /width="1693" height="929"/);
  assert.match(source, /A better way to put coding agents to work\./);
  assert.doesNotMatch(source, /machinist-mark\.png|fonts\.googleapis\.com/);
  assert.match(styles, /--mark: #1d6683/);
  assert.match(styles, /\.drawing-machine/);
  assert.match(styles, /\.drawing-system/);
  assert.match(styles, /\.drawing-assembly/);
  assert.doesNotMatch(styles, /#f08b24|#f2ede3|DM Mono|Inter Tight/);
  assert.equal((mark.match(/<path/g) || []).length, 2);
  assert.doesNotMatch(mark, /<circle|<linearGradient/);
  assert.match(socialCard, /width="1200" height="630"/);
  assert.match(socialCard, /#1d6683/);
  assert.match(socialCard, /M32 28h4V18/);
  assert.doesNotMatch(socialCard, /M32 28v12/);
});

test('repository quick start links to existing guides and stays directly runnable', async () => {
  const source = await import('node:fs/promises').then(({ readFile }) => readFile(new URL('../../README.md', import.meta.url), 'utf8'));

  assert.match(source, /\.github\/site\/technical-drawings\.webp/);
  for (const guide of ['docs/README.md', 'docs/configuration.md', 'docs/development.md', 'docs/vm-deployment.md']) {
    assert.match(source, new RegExp(guide.replace('.', '\\.')));
  }
  assert.doesNotMatch(source, /machinist submit|docs\/getting-started|docs\/how-it-works|docs\/control-plane|docs\/secure-self-hosting/);
});
