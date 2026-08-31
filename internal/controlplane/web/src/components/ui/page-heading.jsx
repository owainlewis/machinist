export function PageHeading({ title, description, children }) {
  return <header className="page-heading flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
    <div>
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      {description && <p className="mt-2 max-w-2xl text-sm text-muted-foreground">{description}</p>}
    </div>
    {children}
  </header>;
}

export function QuietState({ title, description, role }) {
  return <div className="quiet-state grid place-items-center px-6 py-14 text-center" role={role}>
    <span className="quiet-state-mark" aria-hidden="true"><i /><i /><i /></span>
    <p className="mt-4 text-sm font-medium text-foreground">{title}</p>
    {description && <p className="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{description}</p>}
  </div>;
}
