import React, { useId, useState } from "react";
import { Button } from "./button";

export function DetailPanel({ label, children, className = "" }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return <section className={className}>
    <Button type="button" variant="ghost" size="sm" className="-ml-3" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>{open ? "Hide" : "View"} {label}</Button>
    <div id={id} hidden={!open} className="mt-3">{children}</div>
  </section>;
}
