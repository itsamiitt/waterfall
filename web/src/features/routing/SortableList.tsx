// features/routing/SortableList.tsx — keyboard-accessible dnd-kit sortable list (doc 09 §6.1:
// "lanes are dnd-kit sortable lists"). PointerSensor + KeyboardSensor make dragging operable
// without a mouse (doc 12 conventions: keyboard-accessible dnd). Reorder math is the pure
// moveItem() helper so it is unit-testable without a DOM.
import { type ReactNode } from "react";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { moveItem } from "./lifecycle";

function Handle({ id, children }: { id: string; children: ReactNode }) {
  const { attributes, listeners, setNodeRef, transform, isDragging } = useSortable({ id });
  const style = transform
    ? { transform: `translate3d(${transform.x}px, ${transform.y}px, 0)` }
    : undefined;
  return (
    <li
      ref={setNodeRef}
      className="rt-sortable-item"
      style={style}
      data-dragging={isDragging || undefined}
    >
      <button
        type="button"
        className="rt-drag-handle"
        aria-label={`Reorder ${id}`}
        {...attributes}
        {...listeners}
      >
        <span aria-hidden="true">⠿</span>
      </button>
      <div className="rt-sortable-body">{children}</div>
    </li>
  );
}

export interface SortableListProps {
  items: readonly string[];
  onReorder: (next: string[]) => void;
  /** Render the row body for a given item id. */
  renderItem: (id: string) => ReactNode;
  ariaLabel: string;
  disabled?: boolean;
}

export function SortableList({ items, onReorder, renderItem, ariaLabel, disabled }: SortableListProps) {
  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  function onDragEnd(e: DragEndEvent) {
    const over = e.over?.id;
    if (!over || over === e.active.id) return;
    onReorder(moveItem(items, String(e.active.id), String(over)));
  }

  if (disabled) {
    return (
      <ul className="rt-sortable" aria-label={ariaLabel}>
        {items.map((id) => (
          <li key={id} className="rt-sortable-item">
            <span className="rt-drag-handle" aria-hidden="true">
              ⠿
            </span>
            <div className="rt-sortable-body">{renderItem(id)}</div>
          </li>
        ))}
      </ul>
    );
  }

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext items={items as string[]} strategy={verticalListSortingStrategy}>
        <ul className="rt-sortable" aria-label={ariaLabel}>
          {items.map((id) => (
            <Handle key={id} id={id}>
              {renderItem(id)}
            </Handle>
          ))}
        </ul>
      </SortableContext>
    </DndContext>
  );
}
