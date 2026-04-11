export interface PTYOpenParams {
  workspaceId: string;
  shell?: string;
  workdir?: string;
  cols?: number;
  rows?: number;
  authRelayToken?: string;
}

export interface PTYOpenResult {
  sessionId: string;
}

export interface PTYWriteResult {
  ok: boolean;
}

export interface PTYResizeResult {
  ok: boolean;
}

export interface PTYCloseResult {
  closed: boolean;
}

export interface PTYDataEvent {
  sessionId: string;
  data: string;
}

export interface PTYExitEvent {
  sessionId: string;
  exitCode: number;
}
