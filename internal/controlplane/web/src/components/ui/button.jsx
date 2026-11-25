import { Slot } from "@radix-ui/react-slot";
import { cva } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg border font-medium transition-colors disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default: "border-primary/80 bg-primary text-primary-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.14)] hover:bg-primary/90",
        outline: "border-border bg-surface text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.04)] hover:bg-muted",
        ghost: "border-transparent text-muted-foreground hover:bg-muted hover:text-foreground",
      },
      size: { default: "h-[34px] px-3 text-[13px]", sm: "h-[30px] gap-1.5 px-2.5 text-[12.5px]", icon: "size-[34px]" },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export function Button({ className, variant, size, asChild = false, ...props }) {
  const Component = asChild ? Slot : "button";
  return <Component className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
