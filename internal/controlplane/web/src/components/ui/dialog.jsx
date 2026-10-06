import { Dialog as BaseDialog } from "@base-ui/react/dialog";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// Modal is a centred dialog with a title, optional description, body and footer actions.
export function Modal({ open, onOpenChange, title, description, children, footer, className }) {
  return <BaseDialog.Root open={open} onOpenChange={onOpenChange}>
    <BaseDialog.Portal>
      <BaseDialog.Backdrop className="fixed inset-0 z-50 bg-black/50 transition-opacity duration-150 data-[ending-style]:opacity-0 data-[starting-style]:opacity-0" />
      <BaseDialog.Popup className={cn("fixed top-1/2 left-1/2 z-50 flex max-h-[min(44rem,calc(100dvh-2rem))] w-[min(32rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl border border-border-strong bg-surface text-foreground shadow-[0_24px_64px_rgb(0_0_0/0.45)] outline-none transition-[scale,opacity] duration-150 data-[ending-style]:scale-[0.98] data-[ending-style]:opacity-0 data-[starting-style]:scale-[0.98] data-[starting-style]:opacity-0", className)}>
        <header className="flex items-start gap-3 px-5 pt-5 pb-3">
          <div className="min-w-0 flex-1">
            <BaseDialog.Title className="text-[length:0.9375rem] font-semibold tracking-tight">{title}</BaseDialog.Title>
            {description && <BaseDialog.Description className="mt-1 text-muted-foreground">{description}</BaseDialog.Description>}
          </div>
          <BaseDialog.Close render={<Button variant="ghost" size="icon" className="-mt-1 -mr-2" aria-label="Close" />}><X className="size-4" /></BaseDialog.Close>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-5">{children}</div>
        {footer && <footer className="flex items-center justify-end gap-2 border-t border-border px-5 py-3">{footer}</footer>}
      </BaseDialog.Popup>
    </BaseDialog.Portal>
  </BaseDialog.Root>;
}

export const ModalClose = BaseDialog.Close;
