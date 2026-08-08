// Command investhub runs the InvestHub personal investment platform:
// a single self-hosted binary serving the REST API, SSE stream and the Vue SPA.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"investhub/internal/api"
	"investhub/internal/cryptox"
	"investhub/internal/store"
)

func main() {
	log.SetFlags(log.Ltime)

	if err := store.Open(); err != nil {
		log.Fatalf("[db] %v", err)
	}
	defer store.DB.Close()

	if err := cryptox.Init(); err != nil {
		log.Fatalf("[crypto] %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "7788"
	}

	srv := api.New()
	httpSrv := &http.Server{
		Addr:              announceAddr(port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// no WriteTimeout: SSE connections are long-lived
	}

	srv.StartScheduler()

	go func() {
		log.Printf("InvestHub running at http://localhost:%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[http] %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	srv.Stop()
}

func announceAddr(port string) string {
	if host := os.Getenv("HOST"); host != "" {
		return host + ":" + port
	}
	return ":" + port
}
