import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";

const plugins = [remarkGfm];
// Links leave the app; agent output is untrusted, so they never carry the opener or referrer.
const components = { a: ({ node, ...props }) => <a {...props} target="_blank" rel="noreferrer noopener" /> };

// Markdown renders agent text. Raw HTML is shown as text, never as markup.
export function Markdown({ children, className }) {
  return <div className={cn("markdown", className)}><ReactMarkdown remarkPlugins={plugins} components={components}>{children || ""}</ReactMarkdown></div>;
}
