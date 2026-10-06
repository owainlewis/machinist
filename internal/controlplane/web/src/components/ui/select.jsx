import { Select as BaseSelect } from "@base-ui/react/select";
import { Check, ChevronsUpDown } from "lucide-react";
import { cn } from "@/lib/utils";

// Select is the one dropdown used across the app. items: [{ value, label, hint? }].
export function Select({ value, onValueChange, items, label, placeholder = "Select", className, disabled = false, size = "sm" }) {
  return <BaseSelect.Root items={items} value={value} onValueChange={(next) => next !== null && onValueChange(next)} disabled={disabled}>
    <BaseSelect.Trigger aria-label={label} className={cn(
      "inline-flex min-w-0 items-center justify-between gap-1.5 rounded-md border border-border-strong bg-surface text-left text-muted-foreground outline-none select-none hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring data-[popup-open]:bg-muted data-[disabled]:opacity-50",
      size === "sm" ? "h-6.5 px-2 text-xs" : "h-8 w-full px-2.5 text-[length:0.8125rem] text-foreground", className)}>
      <BaseSelect.Value className="truncate data-[placeholder]:text-faint" placeholder={placeholder} />
      <BaseSelect.Icon className="flex-none text-faint"><ChevronsUpDown className="size-3" /></BaseSelect.Icon>
    </BaseSelect.Trigger>
    <BaseSelect.Portal>
      <BaseSelect.Positioner className="z-50 outline-none" sideOffset={4} alignItemWithTrigger={false}>
        <BaseSelect.Popup className="min-w-[var(--anchor-width)] origin-[var(--transform-origin)] rounded-lg border border-border-strong bg-surface p-1 text-foreground shadow-[0_8px_24px_rgb(0_0_0/0.28)] outline-none transition-[scale,opacity] duration-100 data-[ending-style]:scale-[0.98] data-[ending-style]:opacity-0 data-[starting-style]:scale-[0.98] data-[starting-style]:opacity-0">
          <BaseSelect.List className="max-h-[min(20rem,var(--available-height))] overflow-y-auto">
            {items.map((item) => <BaseSelect.Item key={item.value} value={item.value} className="grid cursor-default grid-cols-[0.875rem_1fr] items-center gap-2 rounded-md py-1.5 pr-3 pl-2 text-[length:0.78125rem] outline-none select-none data-[highlighted]:bg-selected">
              <BaseSelect.ItemIndicator className="col-start-1"><Check className="size-3.5" /></BaseSelect.ItemIndicator>
              <span className="col-start-2 flex min-w-0 flex-col"><BaseSelect.ItemText className="truncate">{item.label}</BaseSelect.ItemText>{item.hint && <span className="truncate text-xs text-faint">{item.hint}</span>}</span>
            </BaseSelect.Item>)}
          </BaseSelect.List>
        </BaseSelect.Popup>
      </BaseSelect.Positioner>
    </BaseSelect.Portal>
  </BaseSelect.Root>;
}
