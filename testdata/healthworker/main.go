// testdata/healthworker/main.go — minimal HTTP server used by integration tests.
//
// The binary:
//   - Listens on the port given by the PORT env var (default 8080).
//   - Responds with 200 OK on GET /health.
//   - Exits with status 0 on SIGTERM/SIGINT.
//
// It is compiled by integration tests at runtime via `go build ./testdata/healthworker`.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	srv := &http.Server{Handler: mux}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-quit
		_ = srv.Close()
	}()

	log.Printf("healthworker listening on :%s", port)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
