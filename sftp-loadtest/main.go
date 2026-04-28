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
	"strings"
	"syscall"
	"time"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/fdlimit"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	reportsDir := flag.String("reports-dir", "reports", "directory where finished run reports are persisted")
	schedulesDir := flag.String("schedules-dir", "schedules", "directory where pending scheduled runs are persisted; empty string disables the scheduler")
	debug := flag.Bool("debug", false, "expose /debug/pprof endpoints for live profiling (refused on non-loopback bind addresses)")

	tlsCert := flag.String("tls-cert", "", "path to PEM-encoded TLS certificate; if set together with -tls-key, the server runs as HTTPS")
	tlsKey := flag.String("tls-key", "", "path to PEM-encoded TLS private key (paired with -tls-cert)")

	authUser := flag.String("auth-user", "", "if set together with -auth-pass, require HTTP Basic auth on every request")
	authPass := flag.String("auth-pass", "", "password to pair with -auth-user")

	knownHosts := flag.String("known-hosts", "", "OpenSSH-format known_hosts file used to verify SFTP server keys (recommended)")
	insecureHostKey := flag.Bool("insecure-host-key", false, "DANGEROUS: disable SSH host-key verification entirely (only for ephemeral lab tests)")

	trustProxy := flag.String("trust-proxy", "", "comma-separated CIDRs whose X-Forwarded-For header is honoured for rate-limit attribution; empty (default) ignores XFF entirely")

	flag.Parse()

	// Configure trusted-proxy CIDRs for the rate-limit middleware. Without
	// this, any caller could rotate X-Forwarded-For values to bypass the
	// per-IP bucket. Empty flag = trust nothing.
	if *trustProxy != "" {
		cidrs := strings.Split(*trustProxy, ",")
		if err := web.SetTrustedProxies(cidrs); err != nil {
			log.Fatalf("trust-proxy: %v", err)
		}
		log.Printf("trusted proxy CIDRs: %s", *trustProxy)
	}

	// Check (and raise if possible) the process file-descriptor soft limit
	// before any SFTP connections are made. Surfaces the remediation command
	// up front instead of at mystery failure time mid-run.
	fdlimit.Check()

	// SSH host-key verification: prefer known_hosts, fall back to explicit
	// opt-out, otherwise refuse to dial. This is process-wide; the runtime
	// installs the callback into sftpx so every Dial uses it.
	switch {
	case *knownHosts != "":
		if err := sftpx.UseKnownHosts(*knownHosts); err != nil {
			log.Fatalf("known-hosts: %v", err)
		}
		log.Printf("ssh host-key verification: known_hosts=%s", *knownHosts)
	case *insecureHostKey:
		sftpx.AllowAnyHostKey(log.Printf)
	default:
		log.Fatal("ssh host-key verification: pass -known-hosts <path> (recommended) or -insecure-host-key (lab use only)")
	}

	// Resolve to absolute path so logs and HTTP downloads are unambiguous.
	absDir, err := filepath.Abs(*reportsDir)
	if err != nil {
		log.Fatalf("resolve reports-dir: %v", err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		log.Fatalf("create reports-dir: %v", err)
	}
	absSchedules := ""
	if *schedulesDir != "" {
		absSchedules, err = filepath.Abs(*schedulesDir)
		if err != nil {
			log.Fatalf("resolve schedules-dir: %v", err)
		}
		if err := os.MkdirAll(absSchedules, 0o700); err != nil {
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

	tlsEnabled := *tlsCert != "" && *tlsKey != ""
	authEnabled := *authUser != "" && *authPass != ""

	srv := web.NewServer(absDir, absSchedules)
	defer srv.Shutdown()
	// Tell the probe handler where the known_hosts file lives — that's the
	// only place TOFU will append to. When the operator launched in
	// -insecure-host-key mode, this stays empty and the probe handler
	// refuses TOFU requests with a clear error.
	srv.SetKnownHostsPath(*knownHosts)
	handler := srv.Routes()

	if *debug {
		// Refuse to expose pprof on a public bind address. The heap dump
		// includes the in-memory RunConfig (with plaintext passwords), so a
		// public pprof endpoint is a credential disclosure.
		if !isLoopback(host) {
			log.Fatalf("-debug refuses to mount /debug/pprof on non-loopback bind address %q (re-run with -addr 127.0.0.1:%s)", host, port)
		}
		mux := http.NewServeMux()
		mux.Handle("/", handler)
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		handler = mux
		log.Println("debug mode: /debug/pprof/ enabled (loopback only)")
	}

	// Wrap with the security middleware stack: headers, body-size cap, basic
	// auth (optional), CSRF (custom-header check on POSTs), rate-limit on the
	// expensive endpoints. Order matters: outermost is what every request hits
	// first; innermost is what's closest to the handler.
	stack := web.SecurityHeaders(handler, tlsEnabled)
	stack = web.BodySizeLimit(stack)
	if authEnabled {
		stack = web.BasicAuth(stack, *authUser, *authPass)
		log.Printf("HTTP basic auth: enabled (user=%s)", *authUser)
	} else if !isLoopback(host) {
		log.Printf("WARNING: no -auth-user/-auth-pass provided and bind address %q is not loopback; the UI is publicly reachable without authentication", host)
	}
	stack = web.CSRFGuard(stack)
	stack = web.RateLimit(stack)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           stack,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second, // CSV uploads / large schedule POSTs may take a moment
		WriteTimeout:      0,                // long-running CSV downloads need unbounded write
		IdleTimeout:       60 * time.Second,
	}

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
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	log.Printf("sftp-loadtest listening on %s://%s  reports=%s  schedules=%s  auth=%v  tls=%v",
		scheme, net.JoinHostPort(host, port), absDir, schedLog, authEnabled, tlsEnabled)
	var serveErr error
	if tlsEnabled {
		serveErr = httpSrv.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		serveErr = httpSrv.ListenAndServe()
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatal(serveErr)
	}
	log.Println("stopped")
}

// isLoopback reports whether host resolves to a loopback or unspecified
// address — used to gate dangerous endpoints (pprof) and to warn when the UI
// is exposed without authentication.
func isLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Hostnames other than "localhost" — be conservative and treat as public.
		return false
	}
	return ip.IsLoopback()
}
