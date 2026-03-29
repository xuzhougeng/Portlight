package main

import (
	"embed"
	"flag"
	"log"

	"github.com/xuzhougeng/Portlight/internal/remoteviewer"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	httpDevFlag := flag.Bool("http-dev", false, "serve the desktop frontend over HTTP for browser-based development")
	httpListenFlag := flag.String("http-listen", "127.0.0.1:4173", "listen address for HTTP desktop development mode")
	flag.Parse()

	server := remoteviewer.NewServer(remoteviewer.ServerOptions{
		FrontendDistDir: "frontend/dist",
	})
	defer server.Close()

	if *httpDevFlag {
		if err := runHTTPDevServer(*httpListenFlag, server); err != nil {
			log.Fatalf("run http dev server: %v", err)
		}
		return
	}

	desktopApp := NewDesktopApp(server)

	err := wails.Run(&options.App{
		Title:     "Portlight",
		Width:     1500,
		Height:    980,
		MinWidth:  1160,
		MinHeight: 760,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: server.APIHandler(),
		},
		BackgroundColour:         options.NewRGB(244, 238, 228),
		EnableDefaultContextMenu: true,
		OnStartup:                desktopApp.startup,
		OnShutdown:               desktopApp.shutdown,
		Bind: []interface{}{
			desktopApp,
		},
	})
	if err != nil {
		log.Fatalf("run desktop app: %v", err)
	}
}
