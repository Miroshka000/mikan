import * as Menu from "@radix-ui/react-dropdown-menu";
import { ArrowRightLeft, Check, GripVertical } from "lucide-react";
import { motion, useDragControls, useReducedMotion, type PanInfo } from "motion/react";
import { useId, useRef, useState, type ReactNode } from "react";
import { t } from "../../../../i18n";
import { boardColumns, boxAt, type BoardColumn, type BoardService, type Box } from "./board-model";
import type { Target } from "./targets";

/**
 * The services as a board, a column per place they go: as the mode sends them, through the
 * VPN, direct, blocked, through one server. A tile is dragged by its handle into another
 * column with the mouse or a finger (the column under the pointer takes it); from the keyboard
 * and with a tap its menu moves it. A move is read out. sent is the draft's services (a
 * service left out follows the mode), onMove sends one service to a target.
 */
export function ServiceBoard({ services, sent, targets, onMove }: { services: BoardService[]; sent: Record<string, string> | undefined; targets: Target[]; onMove: (id: string, target: string) => void }) {
  const columns = boardColumns(services, sent, targets, (id) => t("settings.routesOnServer", { name: id.replace("node:", "#") }));
  const columnsEl = useRef<(HTMLElement | null)[]>([]);
  // The columns' boxes in the page's coordinates, as the pointer is told, measured when a drag
  // starts: they stand still while it lasts, and a scroll of the page does not move them there.
  const boxes = useRef<(Box | undefined)[]>([]);
  const measure = () => {
    boxes.current = columnsEl.current.map((el) => {
      const r = el?.getBoundingClientRect();
      return r && { left: r.left + window.scrollX, right: r.right + window.scrollX, top: r.top + window.scrollY, bottom: r.bottom + window.scrollY };
    });
  };
  // The column under the tile being dragged: it shows it will take it.
  const [over, setOver] = useState(-1);
  const [said, setSaid] = useState("");
  const columnAt = (info: PanInfo) => boxAt(info.point.x, info.point.y, boxes.current);
  const move = (svc: BoardService, col: BoardColumn) => {
    onMove(svc.id, col.target);
    setSaid(t("settings.routesBoard.moved", { name: svc.name, to: col.label }));
  };
  return (
    <>
      <div className="board">
        {columns.map((col, i) => (
          <Column
            key={col.target}
            col={col}
            pool={i === 0}
            over={over === i}
            ref={(el) => {
              columnsEl.current[i] = el;
            }}
          >
            {col.items.map((svc) => (
              <Tile
                key={svc.id}
                svc={svc}
                columns={columns}
                current={col.target}
                onLift={measure}
                onDrag={(info) => setOver(columnAt(info))}
                onDrop={(info) => {
                  setOver(-1);
                  const to = columns[columnAt(info)];
                  if (to && to.target !== col.target) move(svc, to);
                }}
                onMove={(to) => {
                  move(svc, to);
                  // The tile is made again in its new column: the focus goes to its menu there.
                  requestAnimationFrame(() => document.querySelector<HTMLElement>(`[data-board-move="${CSS.escape(svc.id)}"]`)?.focus());
                }}
              />
            ))}
          </Column>
        ))}
      </div>
      <p className="sr-only" aria-live="polite" aria-atomic="true">
        {said}
      </p>
    </>
  );
}

function Column({ col, pool, over, ref, children }: { col: BoardColumn; pool: boolean; over: boolean; ref: (el: HTMLElement | null) => void; children: ReactNode }) {
  const id = useId();
  return (
    <section ref={ref} className="board-col" data-pool={pool || undefined} data-over={over || undefined} aria-labelledby={id}>
      <h3 className="board-col-title" id={id}>
        <span className="min-w-0 truncate">{col.label}</span>
        <span className="board-count">{col.items.length}</span>
      </h3>
      {col.items.length ? <ul className="board-list">{children}</ul> : <p className="board-empty">{t("settings.routesBoard.empty")}</p>}
    </section>
  );
}

function Tile({
  svc,
  columns,
  current,
  onLift,
  onDrag,
  onDrop,
  onMove,
}: {
  svc: BoardService;
  columns: BoardColumn[];
  current: string;
  onLift: () => void;
  onDrag: (info: PanInfo) => void;
  onDrop: (info: PanInfo) => void;
  onMove: (to: BoardColumn) => void;
}) {
  const controls = useDragControls();
  const reduce = useReducedMotion();
  const [lifted, setLifted] = useState(false);
  return (
    <motion.li
      className="board-tile"
      data-lifted={lifted || undefined}
      drag
      dragListener={false}
      dragControls={controls}
      dragSnapToOrigin
      dragMomentum={false}
      whileDrag={reduce ? undefined : { scale: 1.03 }}
      onDragStart={() => {
        setLifted(true);
        onLift();
      }}
      onDrag={(_, info) => onDrag(info)}
      onDragEnd={(_, info) => {
        setLifted(false);
        onDrop(info);
      }}
    >
      {/* The handle starts a drag of the mouse or the finger; the menu beside moves it from the keyboard. */}
      <span
        className="sortable-handle"
        aria-hidden
        onPointerDown={(e) => {
          e.preventDefault();
          controls.start(e);
        }}
      >
        <GripVertical size={18} />
      </span>
      <span className="text-lg leading-none" aria-hidden>
        {svc.icon}
      </span>
      <span className="board-tile-name" title={svc.name}>
        {svc.name}
      </span>
      <Menu.Root>
        <Menu.Trigger asChild>
          <button type="button" className="icon-btn h-8 w-8" data-board-move={svc.id} aria-label={t("settings.routesBoard.moveNamed", { name: svc.name })} title={t("settings.routesBoard.move")}>
            <ArrowRightLeft size={16} aria-hidden />
          </button>
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
            <Menu.Label className="menu-label">{t("settings.routesBoard.move")}</Menu.Label>
            {columns.map((col) => (
              <Menu.Item key={col.target} className="menu-item" disabled={col.target === current} onSelect={() => onMove(col)}>
                <span className="min-w-0 flex-1 truncate">{col.label}</span>
                {col.target === current ? <Check size={16} aria-hidden /> : null}
              </Menu.Item>
            ))}
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
    </motion.li>
  );
}
