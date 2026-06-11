// threaddump-analyzer — enterprise-grade JVM thread-dump analyzer.
//
// Single static binary, embedded UI, no runtime deps. Same shape as the
// sibling tools in this repo (sftp-loadtest etc.): bind to 127.0.0.1 by
// default, CSRF guard on POSTs, body-size caps, atomic disk writes for
// the persisted session state.
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
	"strconv"
	"syscall"
	"time"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/patterns"
	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/web"
)

const version = "0.2.0"

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	dataDir := flag.String("data-dir", "data", "directory where sessions and uploads persist; empty = in-memory only")
	patternsDir := flag.String("patterns-dir", "", "directory of additional *.json pattern files to load on top of the builtin catalog")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		log.SetFlags(0)
		log.Printf("threaddump-analyzer %s", version)
		return
	}

	if _, port, err := net.SplitHostPort(*addr); err != nil {
		log.Fatalf("bad -addr: %v", err)
	} else if _, err := strconv.Atoi(port); err != nil {
		log.Fatalf("bad port: %v", err)
	}

	absData := ""
	if *dataDir != "" {
		var err error
		absData, err = filepath.Abs(*dataDir)
		if err != nil {
			log.Fatalf("resolve data-dir: %v", err)
		}
		if err := os.MkdirAll(absData, 0o700); err != nil {
			log.Fatalf("create data-dir: %v", err)
		}
	}

	reg, err := patterns.Load(*patternsDir)
	if err != nil {
		log.Fatalf("patterns: %v", err)
	}
	log.Printf("loaded %d pattern rule(s) (builtin + %s)", len(reg.Rules()), patternsLoc(*patternsDir))

	srv := web.NewServer(absData, reg)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down …")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	log.Printf("threaddump-analyzer %s listening on http://%s  data=%s", version, *addr, dataLoc(absData))
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("stopped")
}

func patternsLoc(dir string) string {
	if dir == "" {
		return "(no external dir)"
	}
	return dir
}

func dataLoc(absData string) string {
	if absData == "" {
		return "(in-memory only)"
	}
	return absData
}
