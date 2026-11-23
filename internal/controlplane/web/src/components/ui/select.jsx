import React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

// Select is the app's one select: the browser's own control with a chevron
// drawn over it, so it looks the same everywhere.
export function Select({ onChange, children, className, ...props }) {
  return <span className={cn("select-field", className)}>
    <select className="field-control" onChange={event => onChange(event.target.value)} {...props}>{children}</select>
    <ChevronDown className="select-field-chevron size-4" aria-hidden="true" />
  </span>;
}
