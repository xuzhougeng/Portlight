import { useEffect, useMemo, useState } from "react";

type HtmlPreviewProps = {
  fileName: string;
  fileUrl: string;
  fitToWidth: boolean;
  zoom: number;
};

const baseViewportHeight = 960;
const baseViewportWidth = 1280;

export function HtmlPreview({
  fileName,
  fileUrl,
  fitToWidth,
  zoom
}: HtmlPreviewProps) {
  const [error, setError] = useState("");
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    setError("");
    setIsLoading(true);
  }, [fileUrl]);

  const frameStyle = useMemo(() => {
    const scale = Math.max(zoom, 0.25);
    const baseStyle = {
      minHeight: `${Math.round(baseViewportHeight / scale)}px`,
      transform: `scale(${scale})`,
      transformOrigin: "top left"
    };

    if (fitToWidth) {
      return {
        ...baseStyle,
        width: `${100 / scale}%`
      };
    }

    return {
      ...baseStyle,
      width: `${baseViewportWidth}px`
    };
  }, [fitToWidth, zoom]);

  return (
    <div className="preview-surface">
      <div className="pdf-toolbar">
        <div className="pdf-toolbar-title">{fileName}</div>
      </div>

      {error ? <div className="preview-error">{error}</div> : null}

      <div className="preview-stage html-preview-stage">
        {isLoading ? (
          <div className="preview-placeholder html-preview-loading">
            正在渲染 HTML
          </div>
        ) : null}

        <iframe
          className={`html-preview-frame ${isLoading ? "is-hidden" : ""}`}
          onError={() => {
            setError("HTML 预览失败");
            setIsLoading(false);
          }}
          onLoad={() => {
            setError("");
            setIsLoading(false);
          }}
          sandbox="allow-downloads allow-forms allow-modals allow-popups allow-scripts"
          src={fileUrl}
          style={frameStyle}
          title={fileName}
        />
      </div>

      <div className="preview-meta">
        HTML 以隔离 iframe 渲染，支持同目录与根路径资源预览
      </div>
    </div>
  );
}
