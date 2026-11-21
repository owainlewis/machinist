import assert from 'node:assert/strict';
import test from 'node:test';

import { fallbackCopy, selectCommand, toggleSound } from './site.js';

test('fallbackCopy copies the full command passed to it', () => {
  const expected = "./bin/machinist run --command=task-to-pr --repo=/path/to/repo --prompt='Complete issue #123'";
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
  const button = { dataset: { copy: 'machinist run --command=task-to-pr --repo=/path/to/repo' }, querySelector: () => code };
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

test('toggleSound unmutes and restarts the explainer, then mutes it again', () => {
  let played = 0;
  const video = { muted: true, currentTime: 12, play() { played += 1; } };
  const attributes = {};
  const button = { textContent: 'Sound on', setAttribute(name, value) { attributes[name] = value; } };

  assert.equal(toggleSound(video, button), true);
  assert.equal(video.muted, false);
  assert.equal(video.currentTime, 0);
  assert.equal(played, 1);
  assert.equal(button.textContent, 'Sound off');
  assert.equal(attributes['aria-pressed'], 'true');

  video.currentTime = 7;
  assert.equal(toggleSound(video, button), false);
  assert.equal(video.muted, true);
  assert.equal(video.currentTime, 7);
  assert.equal(played, 1);
  assert.equal(button.textContent, 'Sound on');
  assert.equal(attributes['aria-pressed'], 'false');
});

const read = (path) => import('node:fs/promises').then(({ readFile }) => readFile(new URL(path, import.meta.url), 'utf8'));

test('setup snippets match the quick start in the README', async () => {
  const source = await read('./index.html');
  const readme = await read('../../README.md');
  for (const command of ['go build -o ./bin/machinist ./cmd/machinist', 'machinist init', 'GH_TOKEN=$(gh auth token) machinist start']) {
    assert.ok(source.includes(`data-copy="${command}"`), `site copies ${command}`);
    assert.ok(readme.includes(command), `README has ${command}`);
  }
});

test('public identity matches the web app: Inter, indigo and the M tile, dark only', async () => {
  const source = await read('./index.html');
  const styles = await read('./styles.css');
  const app = await read('../../internal/controlplane/web/src/styles.css');
  const mark = await read('./machinist-mark.svg');
  const socialCard = await read('./social-card.svg');

  for (const token of ['--background: #161618', '--surface: #1d1d20', '--border: #2f2f35', '--primary: #4c6ef5']) {
    assert.ok(app.includes(token), `web app still uses ${token}`);
    assert.ok(styles.includes(token), `site uses ${token}`);
  }
  assert.match(styles, /Inter Variable/);
  assert.match(source, /class="brand-mark"/);
  assert.match(source, /Assign the work\.<br>Get merged pull requests\./);
  assert.match(source, /<meta name="color-scheme" content="dark">/);
  assert.doesNotMatch(source, /technical-drawings|fonts\.googleapis\.com|machinist-bot/);
  assert.doesNotMatch(styles, /Manrope|Newsreader|#1d6683/);
  assert.match(mark, /#4c6ef5/);
  assert.match(socialCard, /width="1200" height="630"/);
  assert.match(socialCard, /#4c6ef5/);
});

test('the explainer video and its poster ship with the site', async () => {
  const { access } = await import('node:fs/promises');
  const source = await read('./index.html');
  assert.match(source, /src="machinist\.mp4"/);
  assert.match(source, /poster="poster\.jpg"/);
  await access(new URL('./public/machinist.mp4', import.meta.url));
  await access(new URL('./public/poster.jpg', import.meta.url));
});

test('repository quick start links to guides that exist', async () => {
  const { access } = await import('node:fs/promises');
  const source = await read('../../README.md');

  assert.match(source, /go build -o \.\/bin\/machinist \.\/cmd\/machinist/);
  assert.match(source, /machinist start/);
  const guides = [...new Set(source.match(/docs\/[\w./-]+\.md/g))];
  assert.ok(guides.includes('docs/README.md'));
  for (const guide of guides) {
    await access(new URL(`../../${guide}`, import.meta.url));
  }
  assert.doesNotMatch(source, /machinist submit|docs\/getting-started|docs\/how-it-works|docs\/control-plane|docs\/secure-self-hosting/);
});
