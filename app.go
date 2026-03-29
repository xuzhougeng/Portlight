package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/xuzhougeng/Portlight/internal/remoteviewer"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type DesktopApp struct {
	ctx    context.Context
	server *remoteviewer.Server
}

func NewDesktopApp(server *remoteviewer.Server) *DesktopApp {
	return &DesktopApp{
		server: server,
	}
}

func (a *DesktopApp) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *DesktopApp) shutdown(context.Context) {
	_ = a.server.Close()
}

func (a *DesktopApp) SaveRemoteFile(sessionID string, remotePath string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("desktop runtime unavailable")
	}

	filename := filepath.Base(remotePath)
	if filename == "." || filename == "/" || filename == "" {
		filename = "download"
	}

	targetPath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: filename,
		Title:           "保存远程文件",
	})
	if err != nil {
		return "", err
	}
	if targetPath == "" {
		return "", nil
	}

	if err := a.server.SaveRemoteFileToPath(sessionID, remotePath, targetPath); err != nil {
		return "", err
	}

	return targetPath, nil
}
