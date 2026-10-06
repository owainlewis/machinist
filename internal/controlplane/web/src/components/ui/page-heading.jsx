import { Spinner } from "@/components/ui/status-icon";

// TopBar is the thin header on every page: a title on the left, at most a few actions on the right.
export function TopBar({ title, children }) {
  return <header className="top-bar">
    <h1 className="top-bar-title">{title}</h1>
    <span className="flex-1" />
    {children}
  </header>;
}

export function QuietState({ title, description, role, loading = false }) {
  return <div className="grid place-items-center px-6 py-14 text-center" role={role}>
    {loading && <Spinner className="mb-3" />}
    <p className="font-medium text-foreground">{title}</p>
    {description && <p className="mt-1 max-w-sm text-xs leading-5 text-faint">{description}</p>}
  </div>;
}

export function ErrorBanner({ children }) {
  return <div role="alert" className="border-b border-danger/30 bg-danger/10 px-4 py-2 text-danger">{children}</div>;
}
