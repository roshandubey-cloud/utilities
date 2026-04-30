// Mock FTP / FTPS server CLI — thin flag-parsing wrapper around
// internal/mockftp. Used by the e2e suite (global-setup spawns one
// instance for plain-FTP + AUTH-TLS testing and a second for implicit
// FTPS) and by manual smoke runs.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/mockftp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2121", "plain FTP listen address (use empty string to disable plain mode)")
	delay := flag.Duration("trackid-delay", 2*time.Second, "delay before appending #trackid and routing")
	pairs := flag.String("pairs", "", `routing pairs, e.g. "up1=dl1,up2=dl2"; unpaired users self-loop`)
	failUsers := flag.String("fail-users", "", "comma-separated usernames whose uploads always fail (test harness)")
	enableExplicit := flag.Bool("explicit-tls", false, "honour AUTH TLS upgrade on the plain control channel")
	enableImplicit := flag.Bool("implicit-tls", false, "additionally serve TLS-from-byte-0 on -implicit-addr")
	implicitAddr := flag.String("implicit-addr", "127.0.0.1:9990", "implicit-TLS listen address")
	flag.Parse()

	opts := mockftp.Options{
		Addr:      *addr,
		Delay:     *delay,
		Pairs:     mockftp.ParsePairs(*pairs),
		FailUsers: mockftp.ParseFailUsers(*failUsers),
	}
	if *enableExplicit || *enableImplicit {
		opts.TLS = &mockftp.TLSOptions{
			EnableExplicit: *enableExplicit,
			EnableImplicit: *enableImplicit,
			ImplicitAddr:   *implicitAddr,
		}
	}
	srv, err := mockftp.Start(opts)
	if err != nil {
		log.Fatalf("mockftp start: %v", err)
	}
	log.Printf("mockftpserver fingerprint=%s", srv.Fingerprint())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	if err := srv.Stop(); err != nil {
		log.Printf("stop: %v", err)
	}
}
