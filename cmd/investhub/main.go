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
	"investhub/internal/settings"
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
	settings.Migrate() // 将存量明文密钥（含 webhook_url）重新加密为密文

	port := os.Getenv("PORT")
	if port == "" {
		port = "7788"
	}

	srv := api.New()
	addr := announceAddr(port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// no WriteTimeout: SSE connections are long-lived
	}

	// Warn loudly when the service is bound to a non-loopback interface: in
	// that case the whole investment dataset is reachable from the LAN/WAN and
	// a PIN + TLS reverse proxy must be configured.
	if host := os.Getenv("HOST"); host != "" && host != "127.0.0.1" && host != "localhost" {
		log.Printf("[WARN] 监听地址 %s 对局域网/公网开放；请确保已设置访问口令并前置 TLS 反向代理，否则投资数据可能被同网段读取", addr)
	}

	srv.StartScheduler()

	go func() {
		log.Printf("InvestHub running at http://%s", addr)
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
	// Default to the loopback interface so a fresh install is NOT exposed on
	// the LAN. Bind to a wider interface only when the operator explicitly sets
	// HOST (and, in that case, should also configure a PIN + TLS proxy).
	return "127.0.0.1:" + port
}
