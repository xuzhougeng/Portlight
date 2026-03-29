import { FitAddon } from "@xterm/addon-fit";
import { useEffect, useRef } from "react";
import { Terminal } from "xterm";
import "xterm/css/xterm.css";
import type { ActiveSession, TerminalCommandResponse } from "../types";

type ThemeMode = "light" | "dark";

type TerminalPanelProps = {
  currentDir: string;
  session: ActiveSession | null;
  theme: ThemeMode;
};

function getTerminalTheme(theme: ThemeMode) {
  return theme === "dark"
    ? {
        background: "#071018",
        foreground: "#e7eef4",
        cursor: "#d08a69",
        cursorAccent: "#071018",
        selectionBackground: "rgba(208, 138, 105, 0.26)"
      }
    : {
        background: "#101923",
        foreground: "#e5edf4",
        cursor: "#d08a69",
        cursorAccent: "#101923",
        selectionBackground: "rgba(181, 106, 76, 0.24)"
      };
}

function normalizeTerminalOutput(value: string): string {
  return value.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
}

function writeBlock(
  terminal: Terminal,
  value: string,
  options?: { prefix?: string; suffix?: string }
) {
  if (!value) {
    return;
  }

  const normalized = normalizeTerminalOutput(value);
  const content = `${options?.prefix || ""}${normalized}${options?.suffix || ""}`;

  terminal.write(content.replace(/\n/g, "\r\n"));

  if (!content.endsWith("\n")) {
    terminal.write("\r\n");
  }
}

function writeSystemLine(
  terminal: Terminal,
  message: string,
  tone: "info" | "warning" | "error" = "info"
) {
  const color =
    tone === "warning"
      ? "\x1b[38;5;221m"
      : tone === "error"
        ? "\x1b[38;5;203m"
        : "\x1b[38;5;145m";

  terminal.writeln(`${color}${message}\x1b[0m`);
}

function createPrompt(session: ActiveSession, cwd: string): string {
  return `${session.username}@${session.hostname}:${cwd}$ `;
}

function eraseCurrentInput(terminal: Terminal, length: number) {
  if (!length) {
    return;
  }

  terminal.write("\b \b".repeat(length));
}

export function TerminalPanel({
  currentDir,
  session,
  theme
}: TerminalPanelProps) {
  const terminalHostRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const inputBufferRef = useRef("");
  const commandHistoryRef = useRef<string[]>([]);
  const historyIndexRef = useRef(0);
  const isRunningRef = useRef(false);
  const sessionRef = useRef<ActiveSession | null>(session);
  const currentDirRef = useRef(currentDir);

  const effectiveCwd = currentDir || session?.rootPath || "/";

  function showPrompt() {
    const terminal = terminalRef.current;
    const activeSession = sessionRef.current;
    const cwd = currentDirRef.current || activeSession?.rootPath || "/";

    if (!terminal || !activeSession) {
      return;
    }

    inputBufferRef.current = "";
    historyIndexRef.current = commandHistoryRef.current.length;
    terminal.write(createPrompt(activeSession, cwd));
  }

  function replaceInput(nextValue: string) {
    const terminal = terminalRef.current;

    if (!terminal) {
      return;
    }

    eraseCurrentInput(terminal, inputBufferRef.current.length);
    inputBufferRef.current = nextValue;
    terminal.write(nextValue);
  }

  async function runCommand(command: string) {
    const terminal = terminalRef.current;
    const activeSession = sessionRef.current;
    const cwd = currentDirRef.current || activeSession?.rootPath || "/";

    if (!terminal || !activeSession) {
      return;
    }

    isRunningRef.current = true;

    try {
      const response = await fetch("/api/terminal/exec", {
        body: JSON.stringify({
          command,
          cwd,
          sessionId: activeSession.sessionId
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

      const result = (await response.json()) as TerminalCommandResponse;

      if (result.stdout) {
        writeBlock(terminal, result.stdout);
      }

      if (result.stderr) {
        writeBlock(terminal, result.stderr, {
          prefix: "\x1b[38;5;203m",
          suffix: "\x1b[0m"
        });
      }

      if (!result.stdout && !result.stderr) {
        writeSystemLine(terminal, "命令没有返回可显示的输出。");
      }

      if (result.stdoutTruncated || result.stderrTruncated) {
        writeSystemLine(terminal, "输出过长，当前仅保留前 128 KB 内容。", "warning");
      }

      if (result.timedOut) {
        writeSystemLine(terminal, "命令超过 20 秒，已尝试中止远程执行。", "warning");
      }
    } catch (executionError) {
      writeSystemLine(
        terminal,
        executionError instanceof Error
          ? executionError.message
          : "命令执行失败",
        "error"
      );
    } finally {
      isRunningRef.current = false;
      showPrompt();
      fitAddonRef.current?.fit();
      terminal.focus();
    }
  }

  useEffect(() => {
    const element = terminalHostRef.current;

    if (!element) {
      return;
    }

    const terminal = new Terminal({
      allowProposedApi: false,
      convertEol: true,
      cursorBlink: true,
      fontFamily: '"JetBrains Mono", "IBM Plex Mono", monospace',
      fontSize: 13,
      lineHeight: 1.45,
      scrollback: 6000,
      theme: getTerminalTheme(theme)
    });
    const fitAddon = new FitAddon();
    const resizeObserver = new ResizeObserver(() => {
      fitAddon.fit();
    });

    terminal.loadAddon(fitAddon);
    terminal.open(element);
    fitAddon.fit();
    resizeObserver.observe(element);

    const disposable = terminal.onData((data) => {
      if (isRunningRef.current || !sessionRef.current) {
        return;
      }

      if (data === "\r") {
        const command = inputBufferRef.current.trim();

        terminal.write("\r\n");

        if (!command) {
          showPrompt();
          return;
        }

        commandHistoryRef.current.push(command);
        historyIndexRef.current = commandHistoryRef.current.length;
        inputBufferRef.current = "";
        void runCommand(command);
        return;
      }

      if (data === "\u007f") {
        if (!inputBufferRef.current) {
          return;
        }

        inputBufferRef.current = inputBufferRef.current.slice(0, -1);
        terminal.write("\b \b");
        return;
      }

      if (data === "\u0003") {
        eraseCurrentInput(terminal, inputBufferRef.current.length);
        inputBufferRef.current = "";
        terminal.write("^C\r\n");
        showPrompt();
        return;
      }

      if (data === "\u000c") {
        terminal.reset();
        terminal.options.theme = getTerminalTheme(theme);
        showPrompt();
        return;
      }

      if (data === "\u001b[A") {
        if (!commandHistoryRef.current.length || historyIndexRef.current <= 0) {
          return;
        }

        historyIndexRef.current -= 1;
        replaceInput(commandHistoryRef.current[historyIndexRef.current] || "");
        return;
      }

      if (data === "\u001b[B") {
        if (!commandHistoryRef.current.length) {
          return;
        }

        historyIndexRef.current = Math.min(
          historyIndexRef.current + 1,
          commandHistoryRef.current.length
        );
        replaceInput(
          commandHistoryRef.current[historyIndexRef.current] || ""
        );
        return;
      }

      if (/^[\x20-\x7e]$/.test(data)) {
        inputBufferRef.current += data;
        terminal.write(data);
      }
    });

    terminalRef.current = terminal;
    fitAddonRef.current = fitAddon;

    return () => {
      disposable.dispose();
      resizeObserver.disconnect();
      terminal.dispose();
      terminalRef.current = null;
      fitAddonRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (!terminalRef.current) {
      return;
    }

    terminalRef.current.options.theme = getTerminalTheme(theme);
  }, [theme]);

  useEffect(() => {
    sessionRef.current = session;
    currentDirRef.current = effectiveCwd;

    const terminal = terminalRef.current;

    if (!terminal) {
      return;
    }

    terminal.options.theme = getTerminalTheme(theme);
    terminal.reset();
    inputBufferRef.current = "";
    fitAddonRef.current?.fit();

    if (!session) {
      writeSystemLine(terminal, "未连接到服务器，请先在服务器页建立连接。");
      return;
    }

    showPrompt();
    terminal.focus();
  }, [session?.sessionId]);

  useEffect(() => {
    currentDirRef.current = effectiveCwd;
  }, [effectiveCwd]);

  return (
    <div className="preview-surface terminal-shell">
      <div className="terminal-stage">
        <div className="terminal-viewport" ref={terminalHostRef} />
      </div>
    </div>
  );
}
