import { useEffect, useMemo, useRef, useState } from "react";

type ImagePreviewProps = {
  alt: string;
  fitToWidth: boolean;
  src: string;
  zoom: number;
};

export function ImagePreview({
  alt,
  fitToWidth,
  src,
  zoom
}: ImagePreviewProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState(0);
  const [naturalSize, setNaturalSize] = useState({ width: 0, height: 0 });

  useEffect(() => {
    const element = containerRef.current;

    if (!element) {
      return;
    }

    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];

      if (!entry) {
        return;
      }

      setContainerWidth(entry.contentRect.width);
    });

    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    setNaturalSize({ width: 0, height: 0 });
  }, [src]);

  const width = useMemo(() => {
    if (!naturalSize.width) {
      return undefined;
    }

    const baseWidth = fitToWidth
      ? Math.max(containerWidth - 32, 240)
      : naturalSize.width;

    return `${Math.round(baseWidth * zoom)}px`;
  }, [containerWidth, fitToWidth, naturalSize.width, zoom]);

  return (
    <div className="preview-surface" ref={containerRef}>
      <div className="preview-stage">
        <img
          alt={alt}
          className="preview-image"
          onLoad={(event) => {
            setNaturalSize({
              width: event.currentTarget.naturalWidth,
              height: event.currentTarget.naturalHeight
            });
          }}
          src={src}
          style={width ? { width } : undefined}
        />
      </div>
      <div className="preview-meta">
        {naturalSize.width
          ? `${naturalSize.width} × ${naturalSize.height}px`
          : "正在读取图片尺寸"}
      </div>
    </div>
  );
}

