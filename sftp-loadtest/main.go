package main

import (
	"context"
	"flag"
	"fmt"
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
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostkeys"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/web"
)

// platformVersion is bumped on every push so the tester can verify build
// freshness. Also surfaced via the `-version` flag — the SSH-bootstrap
// smoke test on a remote host runs `<bin> -version` to confirm the
// binary it just installed actually executes.
const platformVersion = "0.13.12"

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("sftp-loadtest %s\n", platformVersion)
		return
	}
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	reportsDir := flag.String("reports-dir", "reports", "directory where finished run reports are persisted")
	schedulesDir := flag.String("schedules-dir", "schedules", "directory where pending scheduled runs are persisted; empty string disables the scheduler")
	debug := flag.Bool("debug", false, "expose /debug/pprof endpoints for live profiling (refused on non-loopback bind addresses)")

	tlsCert := flag.String("tls-cert", "", "path to PEM-encoded TLS certificate; if set together with -tls-key, the server runs as HTTPS")
	tlsKey := flag.String("tls-key", "", "path to PEM-encoded TLS private key (paired with -tls-cert)")

	authUser := flag.String("auth-user", "", "if set together with -auth-pass, require HTTP Basic auth on every request")
	authPass := flag.String("auth-pass", "", "password to pair with -auth-user")

	knownHosts := flag.String("known-hosts", "", "OpenSSH-format known_hosts file used to verify SFTP server keys (default: <user-config-dir>/sftp-loadtest/known_hosts, auto-created and managed via the UI's trust-on-first-use flow)")
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

	// SSH host-key verification.
	//
	// Default behaviour (no flags): tool-managed JSON trust store at
	// <config-dir>/sftp-loadtest/hosts.json. The store is the source of
	// truth at runtime; the UI's trust-on-first-use, key-changed-consent,
	// list-trusted, and remove-trusted controls all flow through it.
	// Out-of-band file edits get overwritten on the next change so the
	// UI and the runtime never disagree.
	//
	// -known-hosts <path> keeps the legacy OpenSSH-format file behaviour
	// for CI / shared-fleet setups where the operator manages the file
	// externally. -insecure-host-key remains for ephemeral lab tests.
	var hkStore *hostkeys.Store
	switch {
	case *knownHosts != "":
		if err := sftpx.UseKnownHosts(*knownHosts); err != nil {
			log.Fatalf("known-hosts: %v", err)
		}
		log.Printf("ssh host-key verification: known_hosts=%s (operator-managed file)", *knownHosts)
	case *insecureHostKey:
		sftpx.AllowAnyHostKey(log.Printf)
	default:
		def, derr := defaultHostKeysStorePath()
		if derr != nil {
			log.Fatalf("default trust store path: %v", derr)
		}
		store, oerr := hostkeys.Open(def)
		if oerr != nil {
			log.Fatalf("open trust store %s: %v", def, oerr)
		}
		// Materialise the file at 0o600 on first run so the UI can rely
		// on its existence and an external reader sees our perms.
		if err := store.Save(); err != nil {
			log.Fatalf("init trust store: %v", err)
		}
		hkStore = store
		sftpx.SetHostKeyCallback(store.StrictCallback())
		log.Printf("ssh host-key verification: trust store=%s (managed via UI)", def)
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

	// Crash-resume: if the previous process exited mid-run, the streamed
	// CSV survives but the meta JSON was never written. Synthesise stub
	// metas (marked interrupted=true) so historical runs aren't silently
	// lost. Best-effort — failures here never block startup.
	if recovered, err := persist.RecoverInterrupted(absDir); err == nil && len(recovered) > 0 {
		log.Printf("recovered %d interrupted run(s) from %s: %v", len(recovered), absDir, recovered)
	}

	srv := web.NewServer(absDir, absSchedules)
	defer srv.Shutdown()
	srv.SetVersion(platformVersion)
	// Tell the probe handler where the known_hosts file lives — that's the
	// only place TOFU will append to. When the operator launched in
	// -insecure-host-key mode, this stays empty and the probe handler
	// refuses TOFU requests with a clear error.
	srv.SetKnownHostsPath(*knownHosts)
	if hkStore != nil {
		srv.SetHostKeyStore(hkStore)
	}
	// FTPS leaf-cert TOFU store — sibling of the SSH host-key store, lives
	// under the same OS config dir. Best-effort: a load error logs and
	// keeps cert verification silently disabled (operator can still drive
	// FTPS with the explicit "Trust self-signed cert" toggle).
	if tlsPath, terr := defaultTLSStorePath(); terr == nil {
		if tlsStore, terr := hostkeys.OpenTLS(tlsPath); terr == nil {
			if err := tlsStore.Save(); err != nil {
				log.Printf("init tls trust store %s: %v — FTPS cert TOFU disabled", tlsPath, err)
			} else {
				srv.SetTLSStore(tlsStore)
				log.Printf("ftps cert verification: trust store=%s (managed via UI)", tlsPath)
			}
		} else {
			log.Printf("open tls trust store %s: %v — FTPS cert TOFU disabled", tlsPath, terr)
		}
	}
	handler := srv.Routes()

	if *debug {
		// Refuse to expose pprof on a public bind address. The heap dump
		// includes the in-memory RunConfig (with plaintext passwords), so a
		// public pprof endpoint is a credential disclosure.
		if !isLoopback(host) {
			log.Fatalf("-debug refuses to mount /debug/pprof on non-loopback bind address %q (re-run with -addr 127.0.0.1:%s)", host, port)
		}
		// Loopback alone is not enough — on a shared dev box every local
		// user can reach 127.0.0.1, so the heap dump (and the SSH
		// passwords it contains) would still be readable. Require Basic
		// auth credentials so pprof access has at least the same gate as
		// the rest of the API.
		if !authEnabled {
			log.Fatalf("-debug requires -auth-user and -auth-pass to be set; the heap dump contains plaintext SFTP passwords and must be authenticated even on loopback")
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

// defaultHostKeysStorePath returns the per-user trust-store path inside
// the OS config dir. The UI's controls (TOFU, accept-changed, list,
// delete) all read and write through this store; out-of-band file edits
// are overwritten on the next change.
//
// macOS:   ~/Library/Application Support/sftp-loadtest/hosts.json
// Linux:   ~/.config/sftp-loadtest/hosts.json (respects $XDG_CONFIG_HOME)
// Windows: %AppData%\sftp-loadtest\hosts.json
func defaultHostKeysStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sftp-loadtest", "hosts.json"), nil
}

// defaultTLSStorePath returns the per-user FTPS cert-fingerprint store
// path. Sibling of the SSH host-key store; same operator workflow but
// keyed off TLS leaf-cert SHA-256 instead of SSH host keys.
func defaultTLSStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sftp-loadtest", "tls-hosts.json"), nil
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
