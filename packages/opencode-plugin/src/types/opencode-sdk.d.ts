export interface PluginContext {
  ui: {
    showNotification: (message: string, type: 'info' | 'success' | 'error' | 'warning') => void;
    showModal: (options: { title: string; content: string; buttons?: { label: string; action: string }[] }) => void;
    getInput: (options: { title: string; placeholder?: string }) => Promise<string>;
  };
  events: {
    on: (event: string, handler: (...args: any[]) => void) => void;
    off: (event: string, handler: (...args: any[]) => void) => void;
    emit: (event: string, ...args: any[]) => void;
  };
  state: {
    get: <T>(key: string) => T | undefined;
    set: <T>(key: string, value: T) => void;
    delete: (key: string) => void;
  };
  filesystem: {
    read: (path: string) => Promise<string>;
    write: (path: string, content: string) => Promise<void>;
    exists: (path: string) => Promise<boolean>;
    glob: (pattern: string) => Promise<string[]>;
  };
  hooks: {
    on: (event: string, handler: (...args: any[]) => void | Promise<void>) => void;
    off: (event: string, handler: (...args: any[]) => void | Promise<void>) => void;
  };
}

export interface ToolHookData {
  tool: {
    name: string;
    arguments: any;
    result?: any;
    preventExecution?: boolean;
  };
}

export interface PluginCommand {
  name: string;
  description: string;
  handler: (context: PluginContext, args?: any) => Promise<any>;
}

export interface PluginHook {
  name: string;
  handler: (context: PluginContext, ...args: any[]) => Promise<any>;
}

export interface Plugin {
  name: string;
  version: string;
  onLoad?: (context: PluginContext) => Promise<void>;
  onUnload?: (context: PluginContext) => Promise<void>;
  commands?: PluginCommand[];
  hooks?: PluginHook[];
}

export interface PluginManifest {
  name: string;
  version: string;
  description?: string;
  main: string;
}
