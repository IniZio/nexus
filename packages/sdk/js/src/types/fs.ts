export interface FileStats {
  isFile: boolean;
  isDirectory: boolean;
  size: number;
  mtime: string;
  ctime: string;
  mode: number;
}

export interface FSReadFileParams {
  path: string;
  encoding?: string;
  [key: string]: unknown;
}

export interface FSWriteFileParams {
  path: string;
  content: string | Buffer;
  encoding?: string;
  [key: string]: unknown;
}

export interface FSExistsParams {
  path: string;
  [key: string]: unknown;
}

export interface FSReaddirParams {
  path: string;
  [key: string]: unknown;
}

export interface FSMkdirParams {
  path: string;
  recursive?: boolean;
  [key: string]: unknown;
}

export interface FSRmParams {
  path: string;
  recursive?: boolean;
  [key: string]: unknown;
}

export interface FSStatParams {
  path: string;
  [key: string]: unknown;
}

export interface FSReadFileResult {
  content: string | Buffer;
  encoding: string;
}

export interface FSWriteFileResult {
  success: boolean;
}

export interface FSExistsResult {
  exists: boolean;
}

export interface FSReaddirResult {
  entries: string[];
}

export interface FSMkdirResult {
  success: boolean;
}

export interface FSRmResult {
  success: boolean;
}

export interface FSStatResult {
  stats: FileStats;
}
