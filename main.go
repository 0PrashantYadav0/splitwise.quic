// Command splitwise-quic is a Splitwise clone served entirely over HTTP/3 + QUIC.
//
// It demonstrates a stack of "complex" QUIC techniques:
//   - HTTP/3 over QUIC with TLS 1.3 (mandatory for QUIC)
//   - 0-RTT session resumption for instant reconnects
//   - QUIC DATAGRAM frames (RFC 9221) via WebTransport for live updates
//   - High stream-multiplexing limits (no head-of-line blocking)
//   - Connection migration friendliness (keep-alive + path validation)
//   - Alt-Svc advertisement so browsers upgrade TCP -> QUIC automatically
//   - Optional mutual TLS (set REQUIRE_MTLS=1)
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"splitwise-quic/internal/db"
	"splitwise-quic/internal/handlers"
	"splitwise-quic/internal/realtime"
	"splitwise-quic/internal/render"
	"splitwise-quic/internal/server"
	"splitwise-quic/internal/store"
)

func main() {
	addr := flag.String("addr", ":4433", "host:port to listen on (TCP+UDP)")
	dbPath := flag.String("db", "splitwise.db", "path to the SQLite database file")
	uploads := flag.String("uploads", "uploads", "directory for uploaded receipt images")
	flag.Parse()

	if err := os.MkdirAll(*uploads, 0o755); err != nil {
		log.Fatalf("uploads dir: %v", err)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	renderer, err := render.New()
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	st := store.New(database)
	hub := realtime.NewHub()
	h := handlers.New(st, renderer, hub, *uploads)

	cfg := server.Config{
		Addr:              *addr,
		Hosts:             []string{"localhost", "127.0.0.1", "::1"},
		RequireClientCert: os.Getenv("REQUIRE_MTLS") == "1",
	}

	srv, err := server.New(cfg, func(s *server.Server) http.Handler {
		return h.Routes(s)
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	go func() {
		log.Printf("Splitwise-QUIC ready -> https://localhost%s", *addr)
		log.Printf("(self-signed dev cert; browser will warn once - accept to proceed)")
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Graceful shutdown on Ctrl-C / SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
