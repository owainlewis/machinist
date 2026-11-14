import { Slot } from "@radix-ui/react-slot";
import { cva } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-lg border text-sm font-medium transition-colors disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground hover:bg-primary/90",
        outline: "border-border bg-surface hover:bg-muted",
        ghost: "border-transparent text-muted-foreground hover:bg-muted hover:text-foreground",
      },
      size: { default: "h-[38px] px-3.5", sm: "h-8 gap-1.5 px-2.5 text-[13px]", icon: "size-[38px]" },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export function Button({ className, variant, size, asChild = false, ...props }) {
  const Component = asChild ? Slot : "button";
  return <Component className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
