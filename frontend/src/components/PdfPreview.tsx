import { useEffect, useMemo, useRef, useState } from "react";
import { GlobalWorkerOptions, getDocument } from "pdfjs-dist";

GlobalWorkerOptions.workerSrc = new URL(
  "pdfjs-dist/build/pdf.worker.min.mjs",
  import.meta.url
).toString();

type LoadedPdfDocument = Awaited<ReturnType<typeof getDocument>["promise"]>;

type PdfPreviewProps = {
  fileName: string;
  fileUrl: string;
  fitToWidth: boolean;
  zoom: number;
};

export function PdfPreview({
  fileName,
  fileUrl,
  fitToWidth,
  zoom
}: PdfPreviewProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState(0);
  const [pdfDocument, setPdfDocument] = useState<LoadedPdfDocument | null>(
    null
  );
  const [pageNumber, setPageNumber] = useState(1);
  const [totalPages, setTotalPages] = useState(0);
  const [isLoading, setIsLoading] = useState(true);
  const [isRendering, setIsRendering] = useState(false);
  const [error, setError] = useState("");

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
    let activeDocument: LoadedPdfDocument | null = null;
    let cancelled = false;
    let loadingTask: ReturnType<typeof getDocument> | null = null;

    async function loadPdf() {
      setIsLoading(true);
      setError("");
      setPdfDocument(null);
      setPageNumber(1);
      setTotalPages(0);

      try {
        const response = await fetch(fileUrl);

        if (!response.ok) {
          throw new Error(`PDF 拉取失败: ${response.status}`);
        }

        const data = await response.arrayBuffer();
        loadingTask = getDocument({ data });
        const pdf = await loadingTask.promise;

        if (cancelled) {
          await pdf.destroy();
          return;
        }

        activeDocument = pdf;
        setPdfDocument(pdf);
        setTotalPages(pdf.numPages);
      } catch (loadError) {
        if (!cancelled) {
          setError(loadError instanceof Error ? loadError.message : "PDF 加载失败");
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    }

    void loadPdf();

    return () => {
      cancelled = true;

      if (loadingTask) {
        loadingTask.destroy();
      }

      if (activeDocument) {
        void activeDocument.destroy();
      }
    };
  }, [fileUrl]);

  useEffect(() => {
    if (!pdfDocument || !canvasRef.current || !containerWidth) {
      return;
    }

    const activeDocument = pdfDocument;
    let cancelled = false;
    let activeTask: { cancel: () => void; promise: Promise<unknown> } | null =
      null;

    async function renderPage() {
      setIsRendering(true);
      setError("");

      try {
        const page = await activeDocument.getPage(pageNumber);

        if (cancelled) {
          return;
        }

        const unscaledViewport = page.getViewport({ scale: 1 });
        const baseScale = fitToWidth
          ? Math.max((containerWidth - 32) / unscaledViewport.width, 0.1)
          : 1;
        const cssScale = Math.max(baseScale * zoom, 0.1);
        const devicePixelRatio = window.devicePixelRatio || 1;
        const viewport = page.getViewport({
          scale: cssScale * devicePixelRatio
        });
        const canvas = canvasRef.current;

        if (!canvas) {
          return;
        }

        const context = canvas.getContext("2d");

        if (!context) {
          throw new Error("Canvas 上下文初始化失败");
        }

        canvas.width = Math.floor(viewport.width);
        canvas.height = Math.floor(viewport.height);
        canvas.style.width = `${Math.floor(unscaledViewport.width * cssScale)}px`;
        canvas.style.height = `${Math.floor(unscaledViewport.height * cssScale)}px`;
        context.setTransform(1, 0, 0, 1, 0, 0);
        context.clearRect(0, 0, canvas.width, canvas.height);

        activeTask = page.render({
          canvasContext: context,
          viewport
        });

        await activeTask.promise;
      } catch (renderError) {
        if (!cancelled) {
          const message =
            renderError instanceof Error ? renderError.message : "PDF 渲染失败";

          if (!message.toLowerCase().includes("cancel")) {
            setError(message);
          }
        }
      } finally {
        if (!cancelled) {
          setIsRendering(false);
        }
      }
    }

    void renderPage();

    return () => {
      cancelled = true;

      if (activeTask) {
        activeTask.cancel();
      }
    };
  }, [containerWidth, fitToWidth, pageNumber, pdfDocument, zoom]);

  const pageLabel = useMemo(() => {
    if (!totalPages) {
      return "0 / 0";
    }

    return `${pageNumber} / ${totalPages}`;
  }, [pageNumber, totalPages]);

  return (
    <div className="preview-surface" ref={containerRef}>
      <div className="pdf-toolbar">
        <div className="pdf-toolbar-title">{fileName}</div>
        <div className="pdf-toolbar-controls">
          <button
            disabled={pageNumber <= 1}
            onClick={() => setPageNumber((value) => Math.max(value - 1, 1))}
            type="button"
          >
            上一页
          </button>
          <span>{pageLabel}</span>
          <button
            disabled={pageNumber >= totalPages}
            onClick={() =>
              setPageNumber((value) => Math.min(value + 1, totalPages || 1))
            }
            type="button"
          >
            下一页
          </button>
        </div>
      </div>

      {error ? <div className="preview-error">{error}</div> : null}

      {isLoading ? (
        <div className="preview-placeholder">正在加载 PDF</div>
      ) : (
        <div className="preview-stage">
          <canvas className="preview-canvas" ref={canvasRef} />
        </div>
      )}

      <div className="preview-meta">
        {isRendering ? "正在渲染页面" : `已载入 ${totalPages} 页`}
      </div>
    </div>
  );
}
