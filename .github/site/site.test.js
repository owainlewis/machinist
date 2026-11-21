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
  const copied = [...source.matchAll(/data-copy="([^"]+)"/g)].map(([, value]) => value.replaceAll('&amp;', '&').replaceAll('&quot;', '"'));
  assert.deepEqual(copied, [
    'go build -o ./bin/machinist ./cmd/machinist && export PATH="$PWD/bin:$PATH"',
    'machinist init',
    'gh api user --jq .login',
    'GH_TOKEN=$(gh auth token) machinist start',
  ]);
  // Each part of each command is a line of the README's quick start, so the bare machinist command is on PATH.
  for (const part of copied.flatMap((command) => command.split(' && '))) {
    assert.ok(readme.includes(part), `README has ${part}`);
  }
  // Commands and settings named in the setup prose are the README's too, so a runner set up from the site starts.
  const setup = source.slice(source.indexOf('class="setup-steps"'), source.indexOf('</ol>', source.indexOf('class="setup-steps"')));
  for (const [, inline] of [...setup.matchAll(/<p>[^]*?<\/p>/g)].flatMap(([p]) => [...p.matchAll(/<code>([^<]+)<\/code>/g)])) {
    assert.ok(readme.includes(inline.replace('github.handle', 'handle')), `README has ${inline}`);
  }
});

test('the page says what Machinist is and who it is for, in plain words', async () => {
  const source = await read('./index.html');
  const socialCard = await read('./social-card.svg');
  assert.match(source, /<h1 id="hero-title">A software factory that runs on your own servers<\/h1>/);
  assert.match(source, /<h2 id="who-title">Teams and solo developers<\/h2>/);
  for (const text of [source, socialCard]) {
    assert.match(text, /software factory/);
    assert.match(text, /solo developers/);
  }
  // Slogans from the old copy that said nothing about the product.
  assert.doesNotMatch(source, /production line|babysitting|bottleneck|Get merged pull requests|—/);
});

test('the site and README show only the harnesses Machinist supports, with their logos', async () => {
  const source = await read('./index.html');
  const readme = await read('../../README.md');
  const { readdir } = await import('node:fs/promises');
  const harnessDir = new URL('../../internal/harness/', import.meta.url);
  const names = [];
  for (const file of await readdir(harnessDir)) {
    if (!file.endsWith('.go') || file.endsWith('_test.go')) continue;
    const match = (await read(`../../internal/harness/${file}`)).match(/Name\(\) string \{ return "(\w+)" \}/);
    if (match) names.push(match[1]);
  }
  const shown = { claude: 'Claude Code', codex: 'Codex', agy: 'Antigravity', pi: 'Pi' };
  assert.deepEqual(names.sort(), Object.keys(shown).sort(), 'every harness in the code has a card, and no card claims a missing one');
  const section = source.slice(source.indexOf('class="harnesses"'), source.indexOf('</section>', source.indexOf('class="harnesses"')));
  assert.deepEqual([...section.matchAll(/<strong>([^<]+)<\/strong>/g)].map(([, name]) => name), Object.values(shown));
  assert.match(section, /Machinist is not made by or tied to these companies/);
  for (const logo of ['claude', 'openai', 'google']) {
    for (const mode of ['dark', 'light']) {
      assert.ok(readme.includes(`.github/assets/harness-${logo}-${mode}.svg`), `README uses the ${mode} ${logo} logo`);
      await read(`../assets/harness-${logo}-${mode}.svg`);
    }
  }
});

test('public identity matches the web app: Inter, indigo and the M tile, dark only', async () => {
  const source = await read('./index.html');
  const styles = await read('./styles.css');
  const app = await read('../../internal/controlplane/web/src/styles.css');
  const mark = await read('./favicon.svg');
  const socialCard = await read('./social-card.svg');

  for (const token of ['--background: #161618', '--surface: #1d1d20', '--border: #2f2f35', '--primary: #4c6ef5']) {
    assert.ok(app.includes(token), `web app still uses ${token}`);
    assert.ok(styles.includes(token), `site uses ${token}`);
  }
  assert.match(styles, /Inter Variable/);
  assert.match(source, /class="brand-mark"/);
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
