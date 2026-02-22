export abstract class WorkspaceError extends Error {
  abstract readonly code: string;
  abstract readonly statusCode: number;
  abstract readonly retryable: boolean;

  constructor(
    message: string,
    public readonly cause?: Error,
    public readonly context?: Record<string, unknown>,
  ) {
    super(message);
    this.name = this.constructor.name;
  }

  toJSON(): Record<string, unknown> {
    return {
      name: this.name,
      code: this.code,
      statusCode: this.statusCode,
      message: this.message,
      retryable: this.retryable,
      context: this.context,
      cause: this.cause?.message,
    };
  }
}

export class WorkspaceNotFoundError extends WorkspaceError {
  readonly code = 'WS001' as const;
  readonly statusCode = 404;
  readonly retryable = false;

  constructor(name: string, cause?: Error) {
    super(`Workspace "${name}" not found`, cause, { workspaceName: name });
  }
}

export class WorkspaceAlreadyExistsError extends WorkspaceError {
  readonly code = 'WS002' as const;
  readonly statusCode = 409;
  readonly retryable = false;

  constructor(name: string, cause?: Error) {
    super(`Workspace "${name}" already exists`, cause, { workspaceName: name });
  }
}

export class WorkspaceStartError extends WorkspaceError {
  readonly code = 'WS003' as const;
  readonly statusCode = 500;
  readonly retryable = true;

  constructor(name: string, cause?: Error) {
    super(`Failed to start workspace "${name}"`, cause, { workspaceName: name });
  }
}

export class WorkspaceInvalidNameError extends WorkspaceError {
  readonly code = 'WS004' as const;
  readonly statusCode = 400;
  readonly retryable = false;

  constructor(name: string, reason: string, cause?: Error) {
    super(`Invalid workspace name "${name}": ${reason}`, cause, { workspaceName: name, reason });
  }
}

export class WorkspaceInvalidTransitionError extends WorkspaceError {
  readonly code = 'WS005' as const;
  readonly statusCode = 409;
  readonly retryable = false;

  constructor(name: string, from: string, to: string, cause?: Error) {
    super(
      `Cannot transition workspace "${name}" from "${from}" to "${to}"`,
      cause,
      { workspaceName: name, from, to },
    );
  }
}

export class DockerDaemonError extends WorkspaceError {
  readonly code = 'BE001' as const;
  readonly statusCode = 503;
  readonly retryable = true;

  constructor(message: string, cause?: Error) {
    super(`Docker daemon error: ${message}`, cause);
  }
}

export class ContainerError extends WorkspaceError {
  readonly code = 'BE002' as const;
  readonly statusCode = 500;
  readonly retryable = true;

  constructor(containerId: string, message: string, cause?: Error) {
    super(`Container error (${containerId}): ${message}`, cause, { containerId });
  }
}

export class BackendUnavailableError extends WorkspaceError {
  readonly code = 'BE003' as const;
  readonly statusCode = 503;
  readonly retryable = true;

  constructor(backend: string, cause?: Error) {
    super(`Backend "${backend}" is unavailable`, cause, { backend });
  }
}

export class PortAllocationError extends WorkspaceError {
  readonly code = 'PT001' as const;
  readonly statusCode = 409;
  readonly retryable = true;

  constructor(port: number, message: string, cause?: Error) {
    super(`Port allocation error (${port}): ${message}`, cause, { port });
  }
}

export class PortExhaustedError extends WorkspaceError {
  readonly code = 'PT002' as const;
  readonly statusCode = 503;
  readonly retryable = false;

  constructor(cause?: Error) {
    super('No available ports in allocation range', cause);
  }
}

export class ResourceExhaustedError extends WorkspaceError {
  readonly code = 'RS001' as const;
  readonly statusCode = 503;
  readonly retryable = true;

  constructor(resource: string, cause?: Error) {
    super(`Resource exhausted: ${resource}`, cause, { resource });
  }
}

export class StateCorruptionError extends WorkspaceError {
  readonly code = 'ST001' as const;
  readonly statusCode = 500;
  readonly retryable = false;

  constructor(path: string, reason: string, cause?: Error) {
    super(`State corruption at "${path}": ${reason}`, cause, { path, reason });
  }
}

export class StateLockError extends WorkspaceError {
  readonly code = 'ST002' as const;
  readonly statusCode = 423;
  readonly retryable = true;

  constructor(path: string, cause?: Error) {
    super(`Could not acquire lock for state file "${path}"`, cause, { path });
  }
}

export class GitWorktreeError extends WorkspaceError {
  readonly code = 'GT001' as const;
  readonly statusCode = 500;
  readonly retryable = false;

  constructor(operation: string, message: string, cause?: Error) {
    super(`Git worktree ${operation} failed: ${message}`, cause, { operation });
  }
}

export class PermissionDeniedError extends WorkspaceError {
  readonly code = 'AU001' as const;
  readonly statusCode = 403;
  readonly retryable = false;

  constructor(action: string, cause?: Error) {
    super(`Permission denied: ${action}`, cause, { action });
  }
}

export class AuthenticationError extends WorkspaceError {
  readonly code = 'AU002' as const;
  readonly statusCode = 401;
  readonly retryable = false;

  constructor(message: string, cause?: Error) {
    super(`Authentication failed: ${message}`, cause);
  }
}
