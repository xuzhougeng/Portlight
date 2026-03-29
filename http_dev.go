package main

import (
	"log"
	"net/http"

	"github.com/xuzhougeng/Portlight/internal/remoteviewer"
)

func runHTTPDevServer(listenAddr string, server *remoteviewer.Server) error {
	mux := server.HTTPDevMux()
	log.Printf("portlight http dev server listening at http://%s", listenAddr)
	return http.ListenAndServe(listenAddr, mux)
}
