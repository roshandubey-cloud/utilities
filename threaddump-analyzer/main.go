// threaddump-analyzer — enterprise-grade JVM thread-dump analyzer with
// multi-dump diff, frozen-frame hang detection, and ranked findings.
//
// Lives next to sftp-loadtest in the utilities repo and follows the same
// shape: single static binary, embedded UI, no runtime deps, sane defaults
// for security (bind 127.0.0.1; require X-Requested-With on POSTs).
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/web"
)

const version = "0.1.0"

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
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

	srv := web.NewServer()
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second, // dumps can be large
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

	log.Printf("threaddump-analyzer %s listening on http://%s", version, *addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("stopped")
}
