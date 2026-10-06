import { Slot } from "@radix-ui/react-slot";
import { cva } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-md border text-[length:0.78125rem] font-medium transition-colors outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default: "border-foreground bg-foreground text-background hover:opacity-90",
        outline: "border-border-strong bg-transparent text-foreground hover:bg-muted",
        ghost: "border-transparent text-muted-foreground hover:bg-muted hover:text-foreground",
        danger: "border-transparent text-muted-foreground hover:bg-muted hover:text-danger",
      },
      size: { default: "h-7 px-2.5", sm: "h-6 px-2", icon: "size-7" },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export function Button({ className, variant, size, asChild = false, ...props }) {
  const Component = asChild ? Slot : "button";
  return <Component className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
