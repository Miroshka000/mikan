// The panel's select: a <select> as forms write it (value, onChange with e.target.value,
// <option> and <optgroup> children), drawn as the panel's menus in every browser instead
// of the system's list, whose look and width differ from one browser and OS to the next.
import * as Sel from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";
import { Children, Fragment, isValidElement, type ChangeEvent, type CSSProperties, type ReactElement, type ReactNode } from "react";

type Opt = { value: string; label: ReactNode; disabled?: boolean };
type Entry = Opt | { group: ReactNode; items: Opt[] };

type OptionProps = { value?: string | number; disabled?: boolean; children?: ReactNode };
type GroupProps = { label?: ReactNode; children?: ReactNode };

// Radix keeps "" for "nothing chosen", and forms here use "" for a real choice ("—", "by
// default"): it travels under a value no form uses.
const EMPTY = "\u0000";
const toItem = (v: string) => (v === "" ? EMPTY : v);
const fromItem = (v: string) => (v === EMPTY ? "" : v);

const text = (n: ReactNode): string =>
  typeof n === "string" || typeof n === "number" ? String(n) : Array.isArray(n) ? n.map(text).join("") : isValidElement<{ children?: ReactNode }>(n) ? text(n.props.children) : "";

function options(nodes: ReactNode): Opt[] {
  const out: Opt[] = [];
  for (const n of Children.toArray(nodes)) {
    if (!isValidElement(n)) continue;
    if (n.type === Fragment) out.push(...options((n as ReactElement<{ children?: ReactNode }>).props.children));
    else if (n.type === "option") {
      const p = (n as ReactElement<OptionProps>).props;
      out.push({ value: p.value === undefined ? text(p.children) : String(p.value), label: p.children, disabled: p.disabled });
    }
  }
  return out;
}

function entries(nodes: ReactNode): Entry[] {
  const out: Entry[] = [];
  for (const n of Children.toArray(nodes)) {
    if (!isValidElement(n)) continue;
    if (n.type === Fragment) out.push(...entries((n as ReactElement<{ children?: ReactNode }>).props.children));
    else if (n.type === "optgroup") {
      const p = (n as ReactElement<GroupProps>).props;
      out.push({ group: p.label, items: options(p.children) });
    } else out.push(...options([n]));
  }
  return out;
}

export type SelectProps = {
  id?: string;
  name?: string;
  className?: string;
  style?: CSSProperties;
  title?: string;
  value?: string | number;
  defaultValue?: string | number;
  disabled?: boolean;
  required?: boolean;
  onChange?: (e: ChangeEvent<HTMLSelectElement>) => void;
  children?: ReactNode;
  "aria-label"?: string;
  "aria-labelledby"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean | "true" | "false";
};

export function Select({ id, name, className, style, title, value, defaultValue, disabled, required, onChange, children, ...aria }: SelectProps) {
  const list = entries(children);
  const change = (v: string) => {
    const target = { value: fromItem(v), name: name ?? "", id: id ?? "" };
    // The handlers read e.target.value, as they did from the native select.
    onChange?.({ target, currentTarget: target } as unknown as ChangeEvent<HTMLSelectElement>);
  };
  const item = (o: Opt) => (
    <Sel.Item key={o.value} value={toItem(o.value)} disabled={o.disabled} className="menu-item select-item">
      <Sel.ItemText>{o.label}</Sel.ItemText>
      <Sel.ItemIndicator className="select-check">
        <Check size={15} strokeWidth={2.2} aria-hidden />
      </Sel.ItemIndicator>
    </Sel.Item>
  );
  return (
    <Sel.Root
      value={value === undefined ? undefined : toItem(String(value))}
      defaultValue={defaultValue === undefined ? undefined : toItem(String(defaultValue))}
      onValueChange={change}
      disabled={disabled}
      required={required}
      name={name}
    >
      <Sel.Trigger id={id} className={`${className ?? "input"} select-trigger`} style={style} title={title} {...aria}>
        <span className="select-value">
          <Sel.Value />
        </span>
        <Sel.Icon className="select-icon">
          <ChevronDown size={16} aria-hidden />
        </Sel.Icon>
      </Sel.Trigger>
      <Sel.Portal>
        <Sel.Content className="menu glass-strong select-menu" position="popper" sideOffset={6} collisionPadding={12}>
          <Sel.Viewport className="select-viewport">
            {list.map((e, i) =>
              "group" in e ? (
                <Sel.Group key={`g${i}`}>
                  <Sel.Label className="menu-label">{e.group}</Sel.Label>
                  {e.items.map(item)}
                </Sel.Group>
              ) : (
                item(e)
              ),
            )}
          </Sel.Viewport>
        </Sel.Content>
      </Sel.Portal>
    </Sel.Root>
  );
}
