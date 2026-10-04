import React from "react";
import { cn } from "@/lib/utils";
export const Input = React.forwardRef(function Input(
  { className, ...props },
  ref,
) {
  return (
    <input ref={ref} className={cn("field-control", className)} {...props} />
  );
});
export function Select({ className, ...props }) {
  return <select className={cn("field-control", className)} {...props} />;
}
export function Label({ className, ...props }) {
  return <label className={cn("field-label", className)} {...props} />;
}
