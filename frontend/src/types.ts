export type AuthMethod = "key" | "password";

export type ConnectionConfig = {
  authMethod: AuthMethod;
  host: string;
  password: string;
  privateKey: string;
  port: string;
  rememberPassword: boolean;
  rootPath: string;
  username: string;
};

export type ActiveSession = {
  authMethod: AuthMethod;
  host: string;
  hostname: string;
  port: string;
  rootPath: string;
  sessionId: string;
  username: string;
};

export type ConnectionSessionResponse = {
  alias?: string;
  authMethod: AuthMethod;
  hostname: string;
  port: number;
  sessionId: string;
  username: string;
};

export type RemoteEntry = {
  extension?: string;
  kind: "directory" | "file";
  name: string;
  path: string;
  size: number;
};

export type SavedConnectionProfile = {
  authMethod: AuthMethod;
  host: string;
  id: string;
  name: string;
  port: string;
  privateKey: string;
  rememberPassword: boolean;
  rootPath: string;
  username: string;
};

export type TextPreviewPayload =
  | {
      kind: "binary";
      message: string;
      totalSize: number;
    }
  | {
      kind: "table";
      notice: string;
      previewedBytes: number;
      rows: string[][];
      totalSize: number;
      truncated: boolean;
    }
  | {
      content: string;
      kind: "text";
      notice: string;
      previewedBytes: number;
      totalSize: number;
      truncated: boolean;
    };

export type TerminalCommandResponse = {
  command: string;
  cwd: string;
  exitCode: number | null;
  signal: string | null;
  stderr: string;
  stderrTruncated: boolean;
  stdout: string;
  stdoutTruncated: boolean;
  timedOut: boolean;
};
