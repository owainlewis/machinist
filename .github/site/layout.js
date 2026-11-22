// The site's pages share one header and one footer, kept in partials/. Each
// page marks where they go with <!-- site-header --> and <!-- site-footer -->,
// and names itself on <body data-page="...">, so its link in the header is
// marked as the current page.

// pages maps each page's name to its source file and the path it is served at.
export const pages = {
  home: { file: 'index.html', path: '/' },
  how: { file: 'how-it-works/index.html', path: '/how-it-works/' },
  start: { file: 'get-started/index.html', path: '/get-started/' },
};

export function pageName(html) {
  return /<body[^>]*\sdata-page="([^"]+)"/.exec(html)?.[1] || '';
}

export function layout(html, { header, footer }) {
  const page = pageName(html);
  const marked = page ? header.replace(`data-nav="${page}"`, `data-nav="${page}" aria-current="page"`) : header;
  return html.replace('<!-- site-header -->', marked.trim()).replace('<!-- site-footer -->', footer.trim());
}
