import { useEffect, useMemo, useState } from "react";
import type { TextPreviewPayload } from "../types";

type TextPreviewProps = {
  fileName: string;
  fileUrl: string;
  fitToWidth: boolean;
  zoom: number;
};

function formatSize(size: number): string {
  if (!Number.isFinite(size) || size <= 0) {
    return "-";
  }

  const units = ["B", "KB", "MB", "GB"];
  let current = size;
  let unit = units[0];

  for (const nextUnit of units) {
    unit = nextUnit;

    if (current < 1024 || nextUnit === units[units.length - 1]) {
      break;
    }

    current /= 1024;
  }

  return `${current.toFixed(current >= 100 || unit === "B" ? 0 : 1)} ${unit}`;
}

export function TextPreview({
  fileName,
  fileUrl,
  fitToWidth,
  zoom
}: TextPreviewProps) {
  const [payload, setPayload] = useState<TextPreviewPayload | null>(null);
  const [error, setError] = useState("");
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;

    async function loadTextPreview() {
      setIsLoading(true);
      setError("");
      setPayload(null);

      try {
        const response = await fetch(fileUrl);

        if (!response.ok) {
          const body = (await response.json().catch(() => null)) as
            | { error?: string }
            | null;
          throw new Error(body?.error || `文本预览失败: ${response.status}`);
        }

        const nextPayload = (await response.json()) as TextPreviewPayload;

        if (!cancelled) {
          setPayload(nextPayload);
        }
      } catch (loadError) {
        if (!cancelled) {
          setError(
            loadError instanceof Error ? loadError.message : "文本预览失败"
          );
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    }

    void loadTextPreview();

    return () => {
      cancelled = true;
    };
  }, [fileUrl]);

  const textStyle = useMemo(
    () => ({
      fontSize: `${Math.max(12, Math.round(14 * zoom))}px`,
      whiteSpace: fitToWidth ? ("pre-wrap" as const) : ("pre" as const)
    }),
    [fitToWidth, zoom]
  );

  return (
    <div className="preview-surface">
      <div className="pdf-toolbar">
        <div className="pdf-toolbar-title">{fileName}</div>
      </div>

      {error ? <div className="preview-error">{error}</div> : null}

      {isLoading ? (
        <div className="preview-placeholder">正在加载文本预览</div>
      ) : payload?.kind === "binary" ? (
        <div className="preview-placeholder">{payload.message}</div>
      ) : payload?.kind === "table" ? (
        <div className="preview-stage table-preview-stage">
          <table
            className="table-preview-table"
            style={{ fontSize: `${Math.max(12, Math.round(13 * zoom))}px` }}
          >
            <tbody>
              {payload.rows.map((row, rowIndex) => {
                const cellTag = rowIndex === 0 ? "th" : "td";

                return (
                  <tr key={`row-${rowIndex}`}>
                    {row.map((cell, cellIndex) => {
                      const Cell = cellTag;

                      return (
                        <Cell key={`cell-${rowIndex}-${cellIndex}`}>
                          {cell || " "}
                        </Cell>
                      );
                    })}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="preview-stage text-preview-stage">
          <pre className="text-preview-content" style={textStyle}>
            {payload?.content || "空文件"}
          </pre>
        </div>
      )}

      <div className="preview-meta">
        {payload?.kind === "binary"
          ? `文件大小 ${formatSize(payload.totalSize)}`
          : payload?.kind === "table"
            ? payload.notice ||
              `已读取 ${formatSize(payload.previewedBytes)} / ${formatSize(payload.totalSize)}`
          : payload?.kind === "text"
            ? payload.notice ||
              `已读取 ${formatSize(payload.previewedBytes)} / ${formatSize(payload.totalSize)}`
            : "文本文件将以 UTF-8 方式预览"}
      </div>
    </div>
  );
}
