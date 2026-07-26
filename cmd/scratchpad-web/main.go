// scratchpad-web hosts the artifact index site and static artifact files,
// with an fsnotify watcher pushing change events to SSE clients.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"scratchpad/internal/store"
	"scratchpad/internal/watch"
	"scratchpad/internal/web"
)

func main() {
	addr := flag.String("addr", ":8737", "listen address")
	flag.Parse()

	root, err := store.EnsureRoot()
	if err != nil {
		log.Fatalf("ensure root: %v", err)
	}

	hub := watch.NewHub()
	go func() {
		if err := watch.Run(context.Background(), root, hub); err != nil && err != context.Canceled {
			log.Printf("watcher stopped: %v", err)
		}
	}()

	log.Printf("scratchpad serving %s on %s", root, *addr)
	if err := http.ListenAndServe(*addr, web.NewServer(hub)); err != nil {
		log.Fatal(err)
	}
}
