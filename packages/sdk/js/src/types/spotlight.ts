export interface SpotlightExposeOptions {
  service: string;
  remotePort: number;
  localPort: number;
  host?: string;
}

export interface SpotlightForward {
  id: string;
  workspaceId: string;
  service: string;
  remotePort: number;
  localPort: number;
  host: string;
  createdAt: string;
}

export interface SpotlightListResult {
  forwards: SpotlightForward[];
}

export interface SpotlightApplyDefaultsResult {
  forwards: SpotlightForward[];
}

export interface SpotlightApplyComposePortsError {
  service: string;
  hostPort: number;
  targetPort: number;
  message: string;
}

export interface SpotlightApplyComposePortsResult {
  forwards: SpotlightForward[];
  errors: SpotlightApplyComposePortsError[];
}
