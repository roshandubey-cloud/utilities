package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/fdlimit"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	reportsDir := flag.String("reports-dir", "reports", "directory where finished run reports are persisted")
	schedulesDir := flag.String("schedules-dir", "schedules", "directory where pending scheduled runs are persisted; empty string disables the scheduler")
	debug := flag.Bool("debug", false, "expose /debug/pprof endpoints for live profiling (off by default)")
	flag.Parse()

	// Check (and raise if possible) the process file-descriptor soft limit
	// before any SFTP connections are made. Surfaces the remediation command
	// up front instead of at mystery failure time mid-run.
	fdlimit.Check()

	// Resolve to absolute path so logs and HTTP downloads are unambiguous.
	absDir, err := filepath.Abs(*reportsDir)
	if err != nil {
		log.Fatalf("resolve reports-dir: %v", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		log.Fatalf("create reports-dir: %v", err)
	}
	absSchedules := ""
	if *schedulesDir != "" {
		absSchedules, err = filepath.Abs(*schedulesDir)
		if err != nil {
			log.Fatalf("resolve schedules-dir: %v", err)
		}
		if err := os.MkdirAll(absSchedules, 0o755); err != nil {
			log.Fatalf("create schedules-dir: %v", err)
		}
	}

	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("bad -addr: %v", err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		log.Fatalf("bad port: %v", err)
	}

	srv := web.NewServer(absDir, absSchedules)
	defer srv.Shutdown()
	handler := srv.Routes()
	if *debug {
		mux := http.NewServeMux()
		mux.Handle("/", handler)
		// Mirror the net/http/pprof Init pattern explicitly so we can attach
		// handlers to our own mux without importing side-effect style.
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		handler = mux
		log.Println("debug mode: /debug/pprof/ enabled")
	}
	httpSrv := &http.Server{Addr: *addr, Handler: handler}

	// Graceful shutdown: on SIGINT/SIGTERM, Stop the HTTP server so in-flight
	// requests finish, then exit. Any active run has its own teardown path that
	// triggers when its context is cancelled or its Stop() is called — that
	// already flushes the CSV + metadata to disk via the persist package.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down …")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	schedLog := "(disabled)"
	if absSchedules != "" {
		schedLog = absSchedules
	}
	log.Printf("sftp-loadtest listening on http://%s  reports=%s  schedules=%s", net.JoinHostPort(host, port), absDir, schedLog)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("stopped")
}
