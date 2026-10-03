import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import type { ReactNode } from "react";
import { t } from "../i18n";
import { Button } from "./ui";

/** Side sheet on desktop, full-height sheet on phones. Title is required for screen readers. */
export function Drawer({
  open,
  onOpenChange,
  title,
  meta,
  lead,
  children,
  footer,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  title: string;
  meta?: ReactNode;
  lead?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const reduce = useReducedMotion();
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <AnimatePresence>
        {open ? (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="scrim" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.2 }} />
            </Dialog.Overlay>
            <Dialog.Content asChild forceMount aria-describedby={undefined}>
              <motion.aside
                className="drawer glass-strong"
                initial={reduce ? { opacity: 0 } : { x: "110%" }}
                animate={reduce ? { opacity: 1 } : { x: 0 }}
                exit={reduce ? { opacity: 0 } : { x: "110%" }}
                transition={{ type: "spring", stiffness: 380, damping: 40 }}
              >
                <header className="dr-head">
                  {lead}
                  <div className="dr-title min-w-0 flex-1">
                    <Dialog.Title asChild>
                      <h2>{title}</h2>
                    </Dialog.Title>
                    {meta ? <div className="dr-meta">{meta}</div> : null}
                  </div>
                  <Dialog.Close className="icon-btn" aria-label={t("common.close")}>
                    <X size={18} />
                  </Dialog.Close>
                </header>
                <div className="dr-body">{children}</div>
                {footer ? <footer className="dr-foot">{footer}</footer> : null}
              </motion.aside>
            </Dialog.Content>
          </Dialog.Portal>
        ) : null}
      </AnimatePresence>
    </Dialog.Root>
  );
}

export function Confirm({
  open,
  onOpenChange,
  title,
  text,
  confirm,
  danger,
  loading,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  title: string;
  text: ReactNode;
  confirm: string;
  danger?: boolean;
  loading?: boolean;
  onConfirm: () => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <AnimatePresence>
        {open ? (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="scrim" style={{ zIndex: 55 }} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} />
            </Dialog.Overlay>
            <Dialog.Content asChild forceMount>
              {/* motion owns the transform: the centring shift goes with the scale, or the scale
                  would replace the CSS translate and leave the dialog's corner at the centre. */}
              <motion.div
                className="dialog glass-strong"
                initial={{ opacity: 0, scale: 0.96, x: "-50%", y: "-50%" }}
                animate={{ opacity: 1, scale: 1, x: "-50%", y: "-50%" }}
                exit={{ opacity: 0, scale: 0.98, x: "-50%", y: "-50%" }}
                transition={{ duration: 0.18 }}
              >
                <Dialog.Title asChild>
                  <h2>{title}</h2>
                </Dialog.Title>
                <Dialog.Description asChild>
                  <p className="muted mt-2">{text}</p>
                </Dialog.Description>
                <div className="mt-6 flex justify-end gap-2">
                  <Dialog.Close asChild>
                    <Button variant="ghost">{t("common.cancel")}</Button>
                  </Dialog.Close>
                  <Button variant={danger ? "danger-solid" : "primary"} loading={loading} onClick={onConfirm}>
                    {confirm}
                  </Button>
                </div>
              </motion.div>
            </Dialog.Content>
          </Dialog.Portal>
        ) : null}
      </AnimatePresence>
    </Dialog.Root>
  );
}
