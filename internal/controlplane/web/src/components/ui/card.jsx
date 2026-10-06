import { cn } from "@/lib/utils";

export function Card({ className, ...props }) {
  return <div className={cn("rounded-md border border-border bg-surface", className)} {...props} />;
}
