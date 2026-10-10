import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useId, useState, type FormEvent, type ReactNode } from "react";
import { t } from "../i18n";
import { Button, Field } from "./ui";

/** Side sheet on desktop, full-height sheet on phones. Title is required for screen readers. */
export function Drawer({
  open,
  onOpenChange,
  title,
  meta,
  lead,
  children,
  footer,
  wide,
  titleAction,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  title: string;
  /** A small button beside the title, such as renaming the record. */
  titleAction?: ReactNode;
  meta?: ReactNode;
  lead?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  /** A record's card with many sections: a little wider than a form. */
  wide?: boolean;
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
            <Dialog.Content asChild forceMount aria-describedby={undefined} onEscapeKeyDown={keepOpenForLocalEscape}>
              <motion.aside
                className={wide ? "drawer wide glass-strong" : "drawer glass-strong"}
                initial={reduce ? { opacity: 0 } : { x: "110%" }}
                animate={reduce ? { opacity: 1 } : { x: 0 }}
                exit={reduce ? { opacity: 0 } : { x: "110%" }}
                transition={{ type: "spring", stiffness: 380, damping: 40 }}
              >
                <header className="dr-head">
                  {lead}
                  <div className="dr-title min-w-0 flex-1">
                    <div className="flex min-w-0 items-center gap-1">
                      <Dialog.Title asChild>
                        <h2 className="min-w-0" title={title}>
                          {title}
                        </h2>
                      </Dialog.Title>
                      {titleAction}
                    </div>
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

/**
 * A dialog that asks for one name: renaming a client or a device. `optional` lets it be
 * emptied (the device goes back to the name its app reports, shown as the placeholder).
 * `error` is the API's word on the name; the dialog stays open with it.
 */
export function NameDialog({
  open,
  onOpenChange,
  title,
  label,
  initial,
  placeholder,
  hint,
  maxLength,
  optional,
  error,
  loading,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  title: string;
  label: string;
  initial: string;
  placeholder?: string;
  hint?: ReactNode;
  maxLength: number;
  optional?: boolean;
  error?: string;
  loading?: boolean;
  onSubmit: (name: string) => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <AnimatePresence>
        {open ? (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="scrim" style={{ zIndex: 55 }} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} />
            </Dialog.Overlay>
            <Dialog.Content asChild forceMount aria-describedby={undefined} onEscapeKeyDown={keepOpenForLocalEscape}>
              <motion.div className="dialog glass-strong" initial={{ opacity: 0, scale: 0.96 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.98 }} transition={{ duration: 0.18 }}>
                <Dialog.Title asChild>
                  <h2>{title}</h2>
                </Dialog.Title>
                <NameForm label={label} initial={initial} placeholder={placeholder} hint={hint} maxLength={maxLength} optional={optional} error={error} loading={loading} onSubmit={onSubmit} />
              </motion.div>
            </Dialog.Content>
          </Dialog.Portal>
        ) : null}
      </AnimatePresence>
    </Dialog.Root>
  );
}

function NameForm({
  label,
  initial,
  placeholder,
  hint,
  maxLength,
  optional,
  error,
  loading,
  onSubmit,
}: {
  label: string;
  initial: string;
  placeholder?: string;
  hint?: ReactNode;
  maxLength: number;
  optional?: boolean;
  error?: string;
  loading?: boolean;
  onSubmit: (name: string) => void;
}) {
  const id = useId();
  const [name, setName] = useState(initial);
  const clean = name.trim();
  const ok = (optional || clean !== "") && clean !== initial.trim();
  const send = (e: FormEvent) => {
    e.preventDefault();
    if (ok && !loading) onSubmit(clean);
  };
  return (
    <form onSubmit={send} noValidate className="mt-4">
      <Field label={label} htmlFor={id} hint={hint} error={error}>
        <input
          id={id}
          className="input"
          value={name}
          maxLength={maxLength}
          placeholder={placeholder}
          aria-invalid={!!error}
          autoComplete="off"
          autoFocus
          onFocus={(e) => e.currentTarget.select()}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <div className="mt-6 flex justify-end gap-2">
        <Dialog.Close asChild>
          <Button variant="ghost">{t("common.cancel")}</Button>
        </Dialog.Close>
        <Button variant="primary" type="submit" loading={loading} disabled={!ok}>
          {t("common.save")}
        </Button>
      </div>
    </form>
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
            <Dialog.Content asChild forceMount onEscapeKeyDown={keepOpenForLocalEscape}>
              {/* Centred by CSS layout, not by the transform: motion owns the transform and drops
                  it with reduced motion (Android's battery saver), which left the corner at the centre. */}
              <motion.div
                className="dialog glass-strong"
                initial={{ opacity: 0, scale: 0.96 }}
                animate={{ opacity: 1, scale: 1 }}
                exit={{ opacity: 0, scale: 0.98 }}
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

/**
 * Escape in a field marked data-local-escape (an inline editor inside a drawer or a dialog)
 * is the field's own: it closes the field, the window around it stays open.
 */
function keepOpenForLocalEscape(e: KeyboardEvent) {
  if (e.target instanceof Element && e.target.closest("[data-local-escape]")) e.preventDefault();
}
