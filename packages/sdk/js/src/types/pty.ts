export interface PTYOpenParams {
  workspaceId: string;
  workdir?: string;
  cols?: number;
  rows?: number;
  name?: string;         // Optional display name for the tab
  useTmux?: boolean;    // Whether to use tmux for this session
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

export interface PTYAttachParams {
  sessionId: string;
}

export interface PTYAttachResult {
  attached: boolean;
}

export interface PTYDataEvent {
  sessionId: string;
  data: string;
}

export interface PTYExitEvent {
  sessionId: string;
  exitCode: number;
}

// Session info for multi-tab support
export interface PTYSessionInfo {
  id: string;
  workspaceId: string;
  name: string;
  shell: string;
  workDir: string;
  cols: number;
  rows: number;
  createdAt: string;  // ISO 8601 timestamp
  isRemote: boolean;
  isTmux: boolean;
  tmuxSession?: string;
}

export interface PTYListParams {
  workspaceId: string;
}

export interface PTYListResult {
  sessions: PTYSessionInfo[];
}

export interface PTYGetParams {
  sessionId: string;
}

export interface PTYGetResult {
  session: PTYSessionInfo;
}

export interface PTYRenameParams {
  sessionId: string;
  name: string;
}

export interface PTYRenameResult {
  success: boolean;
}

// Tmux command support
export interface PTYTmuxCommandParams {
  sessionId: string;
  command: string;    // e.g., "new-window", "select-window", "list-windows"
  args?: string[];
}

export interface PTYTmuxCommandResult {
  success: boolean;
  output?: string;
  error?: string;
}
