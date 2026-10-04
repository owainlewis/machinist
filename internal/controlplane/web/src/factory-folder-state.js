const backslash = String.fromCharCode(92);
function pathSeparator(path) {
  const windows =
    /^[A-Za-z]:/.test(path) || path.startsWith(backslash + backslash);
  return windows && path.includes(backslash) ? backslash : "/";
}
export function parentFolder(path) {
  const separator = pathSeparator(path);
  const index = path.lastIndexOf(separator);
  if (index === 2 && /^[A-Za-z]:/.test(path)) return path.slice(0, 3);
  return index < 0 ? "" : path.slice(0, index) || separator;
}
export function cloneDestination(parent, currentPath, gitURL, name) {
  const separator = pathSeparator(parent);
  const existing = (
    separator === backslash
      ? currentPath.replaceAll(backslash, "/")
      : currentPath
  )
    .trim()
    .split("/")
    .filter(Boolean)
    .at(-1);
  const repository = gitURL
    .trim()
    .replace(/\/$/, "")
    .split(/[/:]/)
    .at(-1)
    ?.replace(/\.git$/, "");
  const directory =
    existing || repository || name.trim().replace(/\s+/g, "-") || "repository";
  let base = parent;
  while (base.endsWith(separator)) base = base.slice(0, -1);
  return base + separator + directory;
}
