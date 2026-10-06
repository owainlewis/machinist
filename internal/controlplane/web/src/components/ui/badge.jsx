import { cn } from "@/lib/utils";

export function Badge({ className, ...props }) {
  return <span className={cn("inline-flex h-5 items-center gap-1 rounded-md border border-border-strong px-1.5 text-xs text-muted-foreground", className)} {...props} />;
}
