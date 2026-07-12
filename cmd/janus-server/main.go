// Command janus-server is the JANUS Phase 0 server: it hosts agents, the
// admin web console, and the client-facing API.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nfinit/janus/internal/server"
	"github.com/nfinit/janus/internal/version"
)

func main() {
	var (
		addr   = flag.String("addr", ":8611", "listen address for the console and client API")
		dbPath = flag.String("db", "janus.db", "path to the SQLite database file")
		showV  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showV {
		log.SetFlags(0)
		log.Println(version.Server())
		return
	}

	log.Printf("%s starting", version.Server())

	if dir := filepath.Dir(*dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			log.Fatalf("create db directory: %v", err)
		}
	}

	store, err := server.OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	srv, err := server.New(store)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv.Start(ctx)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: long-poll requests are held open deliberately.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	go func() {
		log.Printf("console + API listening on %s (db: %s)", *addr, *dbPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutCtx)
}
