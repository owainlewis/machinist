import assert from 'node:assert/strict';
import test from 'node:test';

import { fallbackCopy } from './site.js';

test('fallbackCopy copies the full command passed to it', () => {
  const expected = "./bin/machinist run --agent=foreman --repo=/path/to/repo --prompt='Complete issue #123'";
  let input;

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
        remove() {},
      };
    },
    execCommand(command) {
      assert.equal(command, 'copy');
      assert.equal(input.value, expected);
      return true;
    },
  };

  assert.equal(fallbackCopy(expected, fakeDocument), true);
});
