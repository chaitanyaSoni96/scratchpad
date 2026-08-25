// scratchpad-web hosts the artifact index site and static artifact files,
// with an fsnotify watcher pushing change events to SSE clients.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"scratchpad/internal/store"
	"scratchpad/internal/watch"
	"scratchpad/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8737", "listen address")
	flag.Parse()

	root, err := store.EnsureRoot()
	if err != nil {
		log.Fatalf("ensure root: %v", err)
	}

	hub := watch.NewHub()
	watcher, err := watch.New(root, hub)
	if err != nil {
		log.Fatalf("start watcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("scratchpad serving %s on %s", root, *addr)
	server := newHTTPServer(*addr, web.NewServer(hub))
	watchErr := make(chan error, 1)
	serverErr := make(chan error, 1)
	go func() { watchErr <- watcher.Run(ctx) }()
	go func() { serverErr <- server.ListenAndServe() }()

	select {
	case err := <-watchErr:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			log.Printf("shutdown server: %v", shutdownErr)
		}
		if err != nil && err != context.Canceled {
			log.Fatalf("watcher stopped: %v", err)
		}
	case err := <-serverErr:
		cancel()
		<-watchErr
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
