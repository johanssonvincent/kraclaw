export type IPCMessageType =
  | "message"
  | "session_update"
  | "task_create"
  | "task_update"
  | "task_delete"
  | "set_model"
  | "shutdown";

export interface IPCMessage {
  group: string;
  type: IPCMessageType;
  payload: Record<string, unknown>;
  id?: string;
}
