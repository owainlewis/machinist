import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defineConfig } from 'vite';

import { layout, pages } from './layout.js';

const root = import.meta.dirname;
const partial = (name) => readFileSync(resolve(root, 'partials', `${name}.html`), 'utf8');

export default defineConfig({
  plugins: [{
    name: 'machinist-layout',
    transformIndexHtml: {
      order: 'pre',
      handler: (html) => layout(html, { header: partial('header'), footer: partial('footer') }),
    },
  }],
  build: {
    rollupOptions: {
      input: Object.fromEntries(Object.entries(pages).map(([name, page]) => [name, resolve(root, page.file)])),
    },
  },
});
