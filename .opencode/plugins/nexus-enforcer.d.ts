interface PluginContext {
    directory: string;
    client?: {
        app?: {
            log?: (args: {
                body: {
                    service: string;
                    level: string;
                    message: string;
                    extra?: Record<string, unknown>;
                };
            }) => Promise<void>;
        };
        tui?: {
            showToast: (args: {
                body: {
                    title: string;
                    message: string;
                    variant: string;
                    duration: number;
                };
            }) => Promise<void>;
        };
        session?: {
            promptAsync?: (args: {
                path: {
                    id: string;
                };
                body: {
                    parts: Array<{
                        type: string;
                        text: string;
                    }>;
                    agent?: string;
                    model?: {
                        providerID: string;
                        modelID: string;
                    };
                };
                query: {
                    directory: string;
                };
            }) => Promise<unknown>;
        };
    };
    session?: string;
}
interface MessageContent {
    text?: string;
    type?: string;
}
interface OutputMessages {
    messages: Array<{
        content: string | MessageContent;
    }>;
}
interface HookInput {
    tool?: string;
    text?: string;
    event?: {
        type?: string;
        properties?: {
            sessionID?: string;
        };
        session?: string;
    };
    session?: string;
    source?: string;
    role?: string;
    actor?: string;
    isSubAgent?: boolean;
    agentType?: string;
    parentSession?: string;
}
interface HookOutput {
    messages?: Array<{
        content: string | MessageContent;
    }>;
    response?: {
        content: Array<{
            type: string;
            text: string;
        }>;
    };
}
export default function NexusEnforcerPlugin(context: PluginContext): Promise<{
    "tool.execute.before": (input: HookInput, output: HookOutput) => Promise<void>;
    "tool.execute.after": (input: HookInput, output: HookOutput) => Promise<void>;
    "message": (input: HookInput, output: HookOutput) => Promise<void>;
    "experimental.chat.system.transform": (input: HookInput, output: OutputMessages) => Promise<void>;
    "event": (input: HookInput, output: HookOutput) => Promise<void>;
    "chat.input": (input: HookInput, output: HookOutput) => Promise<void>;
}>;
export {};
