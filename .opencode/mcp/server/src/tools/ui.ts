import { Tool } from "@modelcontextprotocol/sdk/types.js";

export interface Toast {
  id: string;
  title: string;
  message: string;
  variant: "info" | "success" | "warning" | "error";
  duration: number;
}

export interface GetToastsInput {
  sessionId: string;
}

export interface GetToastsOutput {
  toasts: Toast[];
}

export interface WaitForToastInput {
  sessionId: string;
  pattern: string;
  timeout?: number;
}

export interface TakeScreenshotInput {
  sessionId: string;
  savePath?: string;
}

export interface ScreenshotOutput {
  path?: string;
  base64?: string;
  format: string;
}

export interface GetTuiStateInput {
  sessionId: string;
}

export interface TuiState {
  panels: {
    id: string;
    type: string;
    focused: boolean;
    dimensions?: {
      width: number;
      height: number;
    };
  }[];
  focusedPanelId?: string;
  cursorPosition?: {
    row: number;
    column: number;
  };
  mode: string;
}

export interface GetTuiStateOutput {
  state: TuiState;
}

export function createGetToastsTool(): Tool {
  return {
    name: "get_toasts",
    description: "Get current toast notifications from the UI",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID to get toasts for",
        },
      },
      required: ["sessionId"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        toasts: {
          type: "array",
          items: {
            type: "object",
            properties: {
              id: { type: "string" },
              title: { type: "string" },
              message: { type: "string" },
              variant: {
                type: "string",
                enum: ["info", "success", "warning", "error"],
              },
              duration: { type: "number" },
            },
            required: ["id", "title", "message", "variant", "duration"],
            additionalProperties: false,
          },
        },
      },
      required: ["toasts"],
      additionalProperties: false,
    },
  };
}

export function createWaitForToastTool(): Tool {
  return {
    name: "wait_for_toast",
    description: "Wait for a toast notification matching a pattern to appear",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID to wait for toasts in",
        },
        pattern: {
          type: "string",
          description: "Pattern to match in toast title or message",
        },
        timeout: {
          type: "number",
          description: "Maximum time to wait in milliseconds (default: 10000)",
          default: 10000,
        },
      },
      required: ["sessionId", "pattern"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        toast: {
          type: "object",
          properties: {
            id: { type: "string" },
            title: { type: "string" },
            message: { type: "string" },
            variant: {
              type: "string",
              enum: ["info", "success", "warning", "error"],
            },
            duration: { type: "number" },
          },
          required: ["id", "title", "message", "variant", "duration"],
          additionalProperties: false,
        },
        matched: { type: "boolean" },
        timedOut: { type: "boolean" },
      },
      required: ["toast", "matched", "timedOut"],
      additionalProperties: false,
    },
  };
}

export function createTakeScreenshotTool(): Tool {
  return {
    name: "take_screenshot",
    description: "Capture a screenshot of the OpenCode window",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID for screenshot context",
        },
        savePath: {
          type: "string",
          description: "Optional path to save the screenshot file",
        },
      },
      required: ["sessionId"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        path: { type: "string" },
        base64: { type: "string" },
        format: { type: "string" },
      },
      required: ["format"],
      additionalProperties: false,
    },
  };
}

export function createGetTuiStateTool(): Tool {
  return {
    name: "get_tui_state",
    description: "Get the current terminal UI state including panels and focus",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID to get TUI state for",
        },
      },
      required: ["sessionId"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        state: {
          type: "object",
          properties: {
            panels: {
              type: "array",
              items: {
                type: "object",
                properties: {
                  id: { type: "string" },
                  type: { type: "string" },
                  focused: { type: "boolean" },
                  dimensions: {
                    type: "object",
                    properties: {
                      width: { type: "number" },
                      height: { type: "number" },
                    },
                    required: ["width", "height"],
                    additionalProperties: false,
                  },
                },
                required: ["id", "type", "focused"],
                additionalProperties: false,
              },
            },
            focusedPanelId: { type: "string" },
            cursorPosition: {
              type: "object",
              properties: {
                row: { type: "number" },
                column: { type: "number" },
              },
              required: ["row", "column"],
              additionalProperties: false,
            },
            mode: { type: "string" },
          },
          required: ["panels", "mode"],
          additionalProperties: false,
        },
      },
      required: ["state"],
      additionalProperties: false,
    },
  };
}

interface SessionState {
  toasts?: Toast[];
  tuiState?: TuiState;
}

declare global {
  var sessionStates: Map<string, SessionState> | undefined;
}

export async function handleGetToasts(
  input: GetToastsInput
): Promise<GetToastsOutput> {
  const sessionState = global.sessionStates?.get(input.sessionId);
  const toasts = sessionState?.toasts || [];

  return { toasts };
}

export async function handleWaitForToast(
  input: WaitForToastInput
): Promise<{ toast: Toast; matched: boolean; timedOut: boolean }> {
  const timeout = input.timeout || 10000;
  const startTime = Date.now();
  const pattern = input.pattern.toLowerCase();

  while (Date.now() - startTime < timeout) {
    const sessionState = global.sessionStates?.get(input.sessionId);
    const toasts = sessionState?.toasts || [];

    const matchedToast = toasts.find((t: Toast) =>
      t.title.toLowerCase().includes(pattern) ||
      t.message.toLowerCase().includes(pattern)
    );

    if (matchedToast) {
      return { toast: matchedToast, matched: true, timedOut: false };
    }

    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  return {
    toast: {
      id: "",
      title: "",
      message: "",
      variant: "info",
      duration: 0,
    },
    matched: false,
    timedOut: true,
  };
}

export async function handleTakeScreenshot(
  input: TakeScreenshotInput
): Promise<ScreenshotOutput> {
  return {
    format: "png",
  };
}

export async function handleGetTuiState(
  input: GetTuiStateInput
): Promise<GetTuiStateOutput> {
  const sessionState = global.sessionStates?.get(input.sessionId);

  const state: TuiState = {
    panels: sessionState?.tuiState?.panels || [],
    focusedPanelId: sessionState?.tuiState?.focusedPanelId,
    cursorPosition: sessionState?.tuiState?.cursorPosition,
    mode: sessionState?.tuiState?.mode || "normal",
  };

  return { state };
}

export {};
