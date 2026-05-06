// Mock SFTP server CLI — thin flag-parsing wrapper around internal/mocksftp.
// The interesting code (filesystem, routing, SFTP handlers) lives in the
// library package so tests can run the server in-process.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/mocksftp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "listen address")
	delay := flag.Duration("trackid-delay", 2*time.Second, "delay before appending #trackid and routing")
	pairs := flag.String("pairs", "", `routing pairs, e.g. "up1=dl1,up2=dl2"; unpaired users self-loop`)
	failUsers := flag.String("fail-users", "", "comma-separated usernames whose uploads always fail (test harness)")
	persist := flag.Bool("persist-content", false, "store uploaded bytes and replay them on download (byte-faithful round trip; off → zero-filled downloads, lower memory)")
	evictAfterRead := flag.Bool("evict-after-read", false, "drop a file's outbox + source-side inbox + sent entries from memory the moment its outbox copy is opened for read; pairs with -persist-content for hours-long hash-verify load runs without unbounded memory growth")
	flag.Parse()

	srv, err := mocksftp.Start(mocksftp.Options{
		Addr:           *addr,
		Delay:          *delay,
		Pairs:          mocksftp.ParsePairs(*pairs),
		FailUsers:      mocksftp.ParseFailUsers(*failUsers),
		PersistContent: *persist,
		EvictAfterRead: *evictAfterRead,
	})
	if err != nil {
		log.Fatalf("mocksftp start: %v", err)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	if err := srv.Stop(); err != nil {
		log.Printf("stop: %v", err)
	}
}
