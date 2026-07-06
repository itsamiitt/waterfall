// features/workflows/SortableSeq.tsx — keyboard-accessible dnd-kit sortable for the sequential
// step of the canvas (doc 09 §7.1). PointerSensor + KeyboardSensor per the P10 conventions.
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

function Item({ id, children }: { id: string; children: ReactNode }) {
  const { attributes, listeners, setNodeRef, transform, isDragging } = useSortable({ id });
  const style = transform
    ? { transform: `translate3d(${transform.x}px, ${transform.y}px, 0)` }
    : undefined;
  return (
    <li ref={setNodeRef} className="wf-seq-item" style={style} data-dragging={isDragging || undefined}>
      <button type="button" className="wf-drag-handle" aria-label={`Reorder ${id}`} {...attributes} {...listeners}>
        <span aria-hidden="true">⠿</span>
      </button>
      {children}
    </li>
  );
}

export interface SortableSeqProps {
  items: readonly string[];
  onReorder: (activeId: string, overId: string) => void;
  renderItem: (id: string) => ReactNode;
  ariaLabel: string;
  disabled?: boolean;
}

export function SortableSeq({ items, onReorder, renderItem, ariaLabel, disabled }: SortableSeqProps) {
  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  function onDragEnd(e: DragEndEvent) {
    const over = e.over?.id;
    if (!over || over === e.active.id) return;
    onReorder(String(e.active.id), String(over));
  }

  if (disabled) {
    return (
      <ul className="wf-seq" aria-label={ariaLabel}>
        {items.map((id) => (
          <li key={id} className="wf-seq-item">
            <span className="wf-drag-handle" aria-hidden="true">⠿</span>
            {renderItem(id)}
          </li>
        ))}
      </ul>
    );
  }

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext items={items as string[]} strategy={verticalListSortingStrategy}>
        <ul className="wf-seq" aria-label={ariaLabel}>
          {items.map((id) => (
            <Item key={id} id={id}>
              {renderItem(id)}
            </Item>
          ))}
        </ul>
      </SortableContext>
    </DndContext>
  );
}
