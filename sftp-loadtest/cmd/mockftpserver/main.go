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
	persist := flag.Bool("persist-content", false, "store uploaded bytes and replay them on RETR (byte-faithful round trip; off → zero-filled downloads, lower memory)")
	evictAfterRead := flag.Bool("evict-after-read", false, "drop a file's outbox + source-side inbox + sent entries from memory the moment its outbox copy is opened for RETR; pairs with -persist-content for hours-long hash-verify load runs without unbounded memory growth")
	passiveAddr := flag.String("passive-addr", "", "IPv4 address advertised in PASV/EPSV responses; default empty → bind 127.0.0.1 (single-host). Set to the container's bridge IP when clients connect from a different Docker container so PASV data dials reach the right host.")
	flag.Parse()

	opts := mockftp.Options{
		Addr:           *addr,
		Delay:          *delay,
		Pairs:          mockftp.ParsePairs(*pairs),
		FailUsers:      mockftp.ParseFailUsers(*failUsers),
		PersistContent: *persist,
		EvictAfterRead: *evictAfterRead,
		PassiveAddr:    *passiveAddr,
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
