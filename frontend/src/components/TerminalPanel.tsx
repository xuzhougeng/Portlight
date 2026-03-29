import { FormEvent, useEffect, useRef, useState } from "react";
import type { ActiveSession, TerminalCommandResponse } from "../types";

type TerminalPanelProps = {
  currentDir: string;
  session: ActiveSession | null;
};

type TerminalHistoryEntry = TerminalCommandResponse & {
  id: string;
  requestedAt: number;
};

const quickCommands = [
  { command: "pwd", label: "pwd" },
  { command: "ls -la", label: "ls -la" },
  { command: "df -h .", label: "df -h ." }
];

function createHistoryId(): string {
  return `${Date.now()}-${Math.random().toString(16).slice(2, 10)}`;
}

function formatStatus(entry: TerminalHistoryEntry): string {
  if (entry.timedOut) {
    return "执行超时";
  }

  if (entry.exitCode === 0) {
    return "执行成功";
  }

  if (entry.exitCode !== null) {
    return `退出码 ${entry.exitCode}`;
  }

  if (entry.signal) {
    return `信号 ${entry.signal}`;
  }

  return "已完成";
}

function formatTimestamp(value: number): string {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false
  }).format(value);
}

export function TerminalPanel({ currentDir, session }: TerminalPanelProps) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [command, setCommand] = useState("");
  const [error, setError] = useState("");
  const [history, setHistory] = useState<TerminalHistoryEntry[]>([]);
  const [isRunning, setIsRunning] = useState(false);

  const effectiveCwd = currentDir || session?.rootPath || "/";

  useEffect(() => {
    setCommand("");
    setError("");
    setHistory([]);
    setIsRunning(false);
  }, [session?.sessionId]);

  useEffect(() => {
    const element = scrollRef.current;

    if (!element) {
      return;
    }

    element.scrollTop = element.scrollHeight;
  }, [history]);

  async function runCommand(nextCommand?: string) {
    if (!session || isRunning) {
      return;
    }

    const commandToRun = (nextCommand ?? command).trim();

    if (!commandToRun) {
      setError("请输入要执行的命令");
      return;
    }

    setError("");
    setIsRunning(true);

    try {
      const response = await fetch("/api/terminal/exec", {
        body: JSON.stringify({
          command: commandToRun,
          cwd: effectiveCwd,
          sessionId: session.sessionId
        }),
        headers: {
          "Content-Type": "application/json"
        },
        method: "POST"
      });

      if (!response.ok) {
        const payload = (await response.json().catch(() => null)) as
          | { error?: string }
          | null;
        throw new Error(payload?.error || `命令执行失败: ${response.status}`);
      }

      const payload = (await response.json()) as TerminalCommandResponse;

      setHistory((value) => [
        ...value,
        {
          ...payload,
          id: createHistoryId(),
          requestedAt: Date.now()
        }
      ]);

      if (!nextCommand) {
        setCommand("");
      }
    } catch (executionError) {
      setError(
        executionError instanceof Error
          ? executionError.message
          : "命令执行失败"
      );
    } finally {
      setIsRunning(false);
    }
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void runCommand();
  }

  return (
    <div className="preview-surface">
      <div className="pdf-toolbar terminal-toolbar">
        <div className="pdf-toolbar-title">远程命令行</div>
        <div className="pdf-toolbar-controls terminal-toolbar-controls">
          <span className="terminal-path" title={effectiveCwd}>
            {effectiveCwd}
          </span>
          <button
            disabled={!history.length}
            onClick={() => {
              setHistory([]);
              setError("");
            }}
            type="button"
          >
            清空输出
          </button>
        </div>
      </div>

      <form className="terminal-form" onSubmit={handleSubmit}>
        <label className="terminal-input-shell">
          <span className="terminal-prompt">
            {session
              ? `${session.username}@${session.hostname}`
              : "未连接"}
          </span>
          <input
            className="terminal-input"
            disabled={!session || isRunning}
            onChange={(event) => setCommand(event.target.value)}
            placeholder="输入命令，例如 tail -n 50 app.log"
            spellCheck={false}
            type="text"
            value={command}
          />
        </label>
        <button disabled={!session || isRunning} type="submit">
          {isRunning ? "执行中" : "执行"}
        </button>
      </form>

      <div className="terminal-quick-actions">
        {quickCommands.map((item) => (
          <button
            className="quick-command-button"
            disabled={!session || isRunning}
            key={item.command}
            onClick={() => {
              void runCommand(item.command);
            }}
            type="button"
          >
            {item.label}
          </button>
        ))}
      </div>

      {error ? <div className="preview-error">{error}</div> : null}

      <div className="preview-stage terminal-stage" ref={scrollRef}>
        {!session ? (
          <div className="terminal-empty">先建立 SSH 会话，再在当前目录执行命令。</div>
        ) : !history.length ? (
          <div className="terminal-empty">
            命令会在当前浏览目录执行。可以直接输入命令，也可以点上面的快捷查询。
          </div>
        ) : (
          <div className="terminal-log">
            {history.map((entry) => {
              const statusClass =
                entry.exitCode === 0 && !entry.timedOut
                  ? "is-success"
                  : "is-error";

              return (
                <article className="terminal-entry" key={entry.id}>
                  <div className="terminal-entry-header">
                    <div className="terminal-command-line">
                      <span className="terminal-command-prompt">
                        {session.username}@{session.hostname}:{entry.cwd}$
                      </span>
                      <span>{entry.command}</span>
                    </div>
                    <span className={`terminal-entry-status ${statusClass}`}>
                      {formatStatus(entry)}
                    </span>
                  </div>
                  <div className="terminal-entry-meta">
                    {formatTimestamp(entry.requestedAt)}
                  </div>

                  {entry.stdout ? (
                    <pre className="terminal-stream">{entry.stdout}</pre>
                  ) : null}
                  {entry.stderr ? (
                    <pre className="terminal-stream is-error">{entry.stderr}</pre>
                  ) : null}
                  {!entry.stdout && !entry.stderr ? (
                    <div className="terminal-note">命令没有返回可显示的输出。</div>
                  ) : null}
                  {entry.stdoutTruncated || entry.stderrTruncated ? (
                    <div className="terminal-note">
                      输出过长，当前仅保留前 128 KB 内容。
                    </div>
                  ) : null}
                  {entry.timedOut ? (
                    <div className="terminal-note">
                      命令超过 20 秒，已尝试中止远程执行。
                    </div>
                  ) : null}
                </article>
              );
            })}
          </div>
        )}
      </div>

      <div className="preview-meta">
        {session
          ? "终端区基于当前文件浏览路径执行单次命令，适合查日志、看进程和做快速排查。"
          : "连接建立后可在这里执行远程命令。"}
      </div>
    </div>
  );
}
