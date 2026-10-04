export function safeLink(value) {
  try {
    const url = new URL(value);
    return ["http:", "https:", "mailto:"].includes(url.protocol) ? value : null;
  } catch {
    return null;
  }
}
export function inlineMarkdown(text) {
  const pattern = /(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^\s]+\))/g;
  const parts = [];
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > cursor)
      parts.push({ type: "text", text: text.slice(cursor, match.index) });
    const value = match[0];
    if (value.startsWith("`"))
      parts.push({ type: "code", text: value.slice(1, -1) });
    else if (value.startsWith("**"))
      parts.push({ type: "strong", text: value.slice(2, -2) });
    else {
      const link = value.match(/^\[([^\]]+)\]\((.+)\)$/);
      const href = safeLink(link[2]);
      parts.push(
        href
          ? { type: "link", text: link[1], href }
          : { type: "text", text: value },
      );
    }
    cursor = match.index + value.length;
  }
  if (cursor < text.length)
    parts.push({ type: "text", text: text.slice(cursor) });
  return parts;
}
export function markdownBlocks(value = "") {
  const lines = String(value).replace(/\r\n/g, "\n").split("\n"),
    blocks = [];
  for (let i = 0; i < lines.length;) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    if (line.startsWith("```")) {
      const language = line.slice(3).trim(),
        code = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```"))
        code.push(lines[i++]);
      if (i < lines.length) i++;
      blocks.push({ type: "code", language, text: code.join("\n") });
      continue;
    }
    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      blocks.push({
        type: "heading",
        level: Math.min(heading[1].length, 4),
        text: heading[2],
      });
      i++;
      continue;
    }
    if (/^\s*([-*+] |\d+\. )/.test(line)) {
      const ordered = /^\s*\d+\. /.test(line),
        items = [];
      while (
        i < lines.length &&
        (ordered ? /^\s*\d+\. / : /^\s*[-*+] /).test(lines[i])
      )
        items.push(lines[i++].replace(/^\s*(?:[-*+]|\d+\.)\s+/, ""));
      blocks.push({ type: "list", ordered, items });
      continue;
    }
    if (line.startsWith("> ")) {
      const text = [];
      while (i < lines.length && lines[i].startsWith("> "))
        text.push(lines[i++].slice(2));
      blocks.push({ type: "quote", text: text.join("\n") });
      continue;
    }
    const text = [line];
    i++;
    while (
      i < lines.length &&
      lines[i].trim() &&
      !/^(#{1,6}\s|```|> |\s*(?:[-*+]|\d+\.)\s)/.test(lines[i])
    )
      text.push(lines[i++]);
    blocks.push({ type: "paragraph", text: text.join("\n") });
  }
  return blocks;
}
export function decodeGitPath(value) {
  if (!value.startsWith('"') || !value.endsWith('"')) return value;
  const bytes = [];
  const encoder = new TextEncoder();
  const quoted = value.slice(1, -1);
  for (let i = 0; i < quoted.length; i++) {
    if (quoted[i] === "\\") {
      const octal = quoted.slice(i + 1, i + 4);
      if (/^[0-7]{3}$/.test(octal)) {
        bytes.push(parseInt(octal, 8));
        i += 3;
        continue;
      }
      const next = quoted[++i];
      bytes.push(
        ...encoder.encode(
          { t: "\t", n: "\n", r: "\r", b: "\b", f: "\f", v: "\v" }[next] ||
            next ||
            "\\",
        ),
      );
    } else bytes.push(...encoder.encode(quoted[i]));
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}
export function parseDiff(value = "") {
  const files = [];
  let file = null,
    oldLine = 0,
    newLine = 0,
    inHunk = false;
  const start = (path) => {
    file = { path, lines: [], added: 0, deleted: 0 };
    files.push(file);
    inHunk = false;
  };
  for (const text of value.replace(/\r\n/g, "\n").split("\n")) {
    if (text.startsWith("diff --git ")) {
      const path = decodeGitPath(
        text.match(/ ("b\/(?:\\.|[^"])+"|b\/.+)$/)?.[1] || "Changes",
      ).replace(/^b\//, "");
      start(path);
      oldLine = 0;
      newLine = 0;
      continue;
    }
    if (!text) continue;
    if (!file) start("Changes");
    if (!inHunk && text.startsWith("+++ ")) {
      if (text !== "+++ /dev/null")
        file.path = decodeGitPath(text.slice(4).replace(/\t.*$/, "")).replace(
          /^b\//,
          "",
        );
      continue;
    }
    if (!inHunk && text.startsWith("--- ")) {
      if (
        file.path === "Changes" ||
        (text.includes("/dev/null") === false && file.path === "/dev/null")
      )
        file.path = decodeGitPath(text.slice(4).replace(/\t.*$/, "")).replace(
          /^a\//,
          "",
        );
      continue;
    }
    const hunk = text.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (hunk) {
      inHunk = true;
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
      file.lines.push({ type: "hunk", text, old: null, new: null });
      continue;
    }
    if (text.startsWith("+")) {
      file.added++;
      file.lines.push({ type: "add", text, old: null, new: newLine++ || null });
    } else if (text.startsWith("-")) {
      file.deleted++;
      file.lines.push({
        type: "delete",
        text,
        old: oldLine++ || null,
        new: null,
      });
    } else if (text.startsWith(" ")) {
      file.lines.push({
        type: "context",
        text,
        old: oldLine++ || null,
        new: newLine++ || null,
      });
    } else if (text)
      file.lines.push({ type: "meta", text, old: null, new: null });
  }
  return files;
}
export function initialReviewTab(task) {
  if (task?.stage?.toLowerCase() === "design") return "design";
  if (task?.status === "failed" && task.checks?.some((check) => !check.passed))
    return "checks";
  return "changes";
}
export function clampPanelWidth(width, viewport = 1200) {
  return Math.max(
    320,
    Math.min(
      Number(width) || 520,
      Math.max(320, Math.min(1000, viewport - 80)),
    ),
  );
}
