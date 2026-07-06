// design/primitives barrel (doc 08 §2). Features compose these; no raw CSS values leave design/.
export { Button, type ButtonProps } from "./Button";
export { Input, type InputProps } from "./Input";
export { Select, type SelectProps, type SelectOption } from "./Select";
export { Table, type TableProps, type ColumnDef, type SortSpec } from "./Table";
export { Modal, type ModalProps } from "./Modal";
export { Drawer, type DrawerProps } from "./Drawer";
export { Tabs, type TabsProps, type TabSpec } from "./Tabs";
export { Toast, ToastRegion, type ToastItem, type ToastKind } from "./Toast";
export { Badge, type BadgeProps } from "./Badge";
export { StatTile, type StatTileProps } from "./StatTile";
export { EmptyState, type EmptyStateProps } from "./EmptyState";
export { ConfirmDialog, type ConfirmDialogProps } from "./ConfirmDialog";
export { CodeBlock, type CodeBlockProps } from "./CodeBlock";
export {
  TimeRangePicker,
  type TimeRangePickerProps,
  type TimeRange,
  type TimePreset,
  type TimeRes,
} from "./TimeRangePicker";
export { Icon, type IconName } from "./Icon";
