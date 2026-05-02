package web

import (
	"context"
	"crypto/x509"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostinfo"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostkeys"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/latency"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/proc"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/protocol"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/report"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/runner"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

//go:embed static
var staticFS embed.FS

// maxRetainedRuns bounds in-memory history so a long-running server doesn't
// accumulate unbounded state. Older runs are dropped oldest-first.
const maxRetainedRuns = 10

type Server struct {
	mu             sync.Mutex
	runs           map[string]*runner.Run
	order          []string // chronological, index 0 = oldest
	procMon        *proc.Monitor
	reportsDir     string
	schedules      *scheduleStore // nil if -schedules-dir wasn't provided
	stopCh         chan struct{}  // closed on shutdown to stop background tickers
	knownHostsPath string         // set by main.go from -known-hosts; "" = file mode disabled
	// hostKeyStore is the tool-managed JSON trust store. When non-nil it is
	// the authoritative source for host-key trust decisions and the legacy
	// file-based code paths are bypassed. When nil, the operator passed
	// -known-hosts (file mode) or -insecure-host-key (no verification).
	hostKeyStore *hostkeys.Store
	// tlsStore is the parallel FTPS leaf-cert fingerprint store. Same
	// semantics as hostKeyStore — first probe captures, the UI prompts,
	// accept appends, future probes verify against it.
	tlsStore *hostkeys.TLSStore
	// version is the platform version string surfaced on /healthz?detail=1
	// so the cluster coordinator can detect worker / master skew. Wired
	// from main.go via SetVersion. Empty until SetVersion is called.
	version string
}

// NewServer constructs the HTTP server. schedulesDir may be empty, in which
// case the scheduler endpoints return 503 and no ticker runs.
func NewServer(reportsDir, schedulesDir string) *Server {
	s := &Server{
		runs:       map[string]*runner.Run{},
		procMon:    proc.New(),
		reportsDir: reportsDir,
		stopCh:     make(chan struct{}),
	}
	if schedulesDir != "" {
		s.schedules = newScheduleStore(schedulesDir)
		go s.scheduleTicker(s.stopCh)
	}
	return s
}

// Shutdown stops background tickers AND tears down every SSH-bootstrapped
// worker tunnel. Call before exiting so the master never leaves orphan
// SSH sessions or remote sftp-loadtest processes behind.
func (s *Server) Shutdown() {
	closeAllSpawned()
	close(s.stopCh)
}

// SetKnownHostsPath records the path the operator passed via -known-hosts.
// Used only in legacy file-mode; the tool-managed JSON store (set via
// SetHostKeyStore) is preferred and bypasses this entirely.
func (s *Server) SetKnownHostsPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownHostsPath = path
}

func (s *Server) getKnownHostsPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.knownHostsPath
}

// SetHostKeyStore wires the tool-managed JSON trust store into the web
// layer. When set, all probe / pre-flight / runtime host-key decisions
// flow through it and the legacy file-based code paths are dormant.
func (s *Server) SetHostKeyStore(store *hostkeys.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostKeyStore = store
}

// HostKeyStoreActive reports whether the tool-managed JSON trust store
// is the active SSH host-key authority. Callers wiring the desktop /
// CLI use this to decide whether to ALSO bind the legacy file-mode
// callback (the UI shows different copy in store mode vs file mode,
// and binding both would silently double-trust).
func (s *Server) HostKeyStoreActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKeyStore != nil
}

func (s *Server) getHostKeyStore() *hostkeys.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKeyStore
}

// SetTLSStore wires the FTPS cert-fingerprint TOFU store. Mirrors the
// SSH host-key store but keyed off TLS leaf-cert SHA-256 instead of
// SSH host keys. Probe + start use it to drive the same TOFU /
// renewal consent UX for FTPS that SFTP already has.
func (s *Server) SetTLSStore(store *hostkeys.TLSStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tlsStore = store
}

func (s *Server) getTLSStore() *hostkeys.TLSStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tlsStore
}

// SetVersion records the platform version string. Surfaced on
// /healthz?detail=1 so a cluster master can compare its own version
// against each worker's during fan-out.
func (s *Server) SetVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
}

func (s *Server) getVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// startedAt is recorded once at construction for the /healthz uptime field.
var processStart = time.Now()

// /api/probe — quick connectivity check before a real run. Tries (in
// order): TCP dial → SSH handshake → SFTP subsystem → optional folder
// listing. Returns per-stage timings and the friendly error of whichever
// stage failed first. Never holds the connection open — opens, validates,
// closes within ~15 s.
//
// Body: {"host","port","username","password","folder"}. Username/password
// optional — if absent, only the TCP stage runs. Folder optional too.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host             string `json:"host"`
		Port             int    `json:"port"`
		Username         string `json:"username"`
		Password         string `json:"password"`
		Folder           string `json:"folder"`
		// TrustOnFirstUse, when true, instructs the probe to ADD this server's
		// host key to the known_hosts file if it isn't there yet. A key that's
		// already known and matches is silently accepted. A key that's already
		// known and DIFFERENT is refused (MITM signal) UNLESS AcceptChanged is
		// also true. Requires the server was launched with -known-hosts <path>.
		TrustOnFirstUse  bool `json:"trust_on_first_use"`
		// AcceptChanged, when true alongside TrustOnFirstUse, removes any
		// existing known_hosts entry for the host before TOFU appends — used
		// by the UI's "host key changed" consent flow after the operator
		// explicitly approves overwriting the previous key.
		AcceptChanged    bool `json:"accept_changed"`
		// PrivateKey, when non-empty, swaps password auth for public-key
		// auth on this probe. Same shape the /api/start RunConfig accepts
		// so the UI can round-trip the same field. Passphrase is optional
		// (only used when the key PEM is encrypted).
		PrivateKey       string `json:"private_key"`
		Passphrase       string `json:"passphrase"`

		// Protocol selects sftp / ftp / ftps. Empty = sftp (back-compat).
		Protocol              string `json:"protocol"`
		TLSMode               string `json:"tls_mode"`
		TLSInsecureSkipVerify bool   `json:"tls_insecure_skip_verify"`
		TLSServerName         string `json:"tls_server_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Host == "" || req.Port <= 0 {
		http.Error(w, "host and port required", http.StatusBadRequest)
		return
	}

	addr := net.JoinHostPort(req.Host, fmt.Sprintf("%d", req.Port))
	out := map[string]any{"ok": false, "host": req.Host, "port": req.Port}

	// Stage 1 — TCP dial. Tightest timeout of the bunch; if we can't reach
	// the box at all, no point trying SSH.
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		log.Printf("probe %s: tcp: %v", addr, err)
		out["stage"] = "tcp"
		out["error"] = friendlyProbeError("tcp", err)
		out["tcp_ms"] = time.Since(t0).Milliseconds()
		writeJSON(w, out)
		return
	}
	out["tcp_ms"] = time.Since(t0).Milliseconds()
	conn.Close()

	// Protocol routing. Empty/"sftp" falls through to the existing SSH+SFTP
	// path so behaviour for SFTP probes is byte-identical to v0.12. FTP and
	// FTPS short-circuit to a separate handler that handles their own
	// connect/auth and surfaces a generic "connect_ms" stage timing.
	proto := protocol.Normalize(req.Protocol)
	out["protocol"] = string(proto)
	if proto == protocol.FTP || proto == protocol.FTPS {
		s.probeFTP(w, out, proto, req.Host, req.Port, req.Username, req.Password, req.Folder, req.TLSMode, req.TLSInsecureSkipVerify, req.TLSServerName, req.TrustOnFirstUse, req.AcceptChanged)
		return
	}

	// If no creds given, stop here — TCP-only probe.
	if req.Username == "" {
		out["ok"] = true
		out["stage"] = "tcp"
		out["note"] = "TCP only — supply username + password to verify SSH/SFTP/auth"
		writeJSON(w, out)
		return
	}

	// Stage 2 + 3 — SSH handshake + SFTP subsystem.
	//
	// Three modes, chosen per-request:
	//   a) TrustOnFirstUse=true + known_hosts set:
	//      Use TOFUCallback — auto-append unknown keys, accept after.
	//   b) TrustOnFirstUse=false + known_hosts set:
	//      Use CapturePreviewCallback — strict check, but on "key unknown"
	//      capture the fingerprint AND respond with requires_consent=true
	//      so the UI can show an explicit Accept/Reject prompt.
	//   c) -insecure-host-key mode (no known_hosts path):
	//      Fall through to the process-wide callback (insecure).
	t1 := time.Now()
	var dialOpts sftpx.DialOpts
	var capturedFP, capturedPrev string
	var capturedChanged bool
	khPath := s.getKnownHostsPath()
	store := s.getHostKeyStore()

	// AcceptChanged: drop the existing trust entry so the TOFU dial below
	// re-records the new key. Done before the SSH dial so strict-checking
	// sees a clean slate. Implementation differs by mode (store vs file)
	// but the user-visible behaviour is identical.
	if req.AcceptChanged && req.TrustOnFirstUse {
		switch {
		case store != nil:
			if _, rerr := store.Remove(req.Host, req.Port); rerr != nil {
				log.Printf("probe %s: remove existing trust entry: %v", addr, rerr)
				out["stage"] = "ssh_or_sftp"
				out["error"] = "could not update trust store to overwrite previous entry"
				writeJSON(w, out)
				return
			}
			log.Printf("probe %s: removed previous trust entry per accept_changed", addr)
		case khPath != "":
			if err := sftpx.RemoveKnownHostEntries(khPath, addr); err != nil {
				log.Printf("probe %s: remove existing known_hosts entry: %v", addr, err)
				out["stage"] = "ssh_or_sftp"
				out["error"] = "could not rewrite known_hosts to overwrite previous entry"
				writeJSON(w, out)
				return
			}
			log.Printf("probe %s: removed previous known_hosts entry per accept_changed", addr)
		}
	}

	switch {
	case req.TrustOnFirstUse && store != nil:
		dialOpts.HostKeyCallback = store.TOFUCallback(func(ck hostkeys.CapturedKey) {
			capturedFP = ck.Fingerprint
		})
	case req.TrustOnFirstUse && khPath != "":
		cb, cberr := sftpx.TOFUCallback(khPath, func(host, fp string) {
			capturedFP = fp
		})
		if cberr != nil {
			out["stage"] = "ssh_or_sftp"
			out["error"] = "tofu setup: " + cberr.Error()
			writeJSON(w, out)
			return
		}
		dialOpts.HostKeyCallback = cb
	case req.TrustOnFirstUse:
		// trust_on_first_use without a trust mechanism: when the server
		// is in -insecure-host-key mode there's no host-key verification
		// happening anyway, so TOFU is a no-op rather than an error.
		// Silently honour the request — the Start preflight calls this
		// path on every run, and turning it into a hard error trapped
		// operators in lab/insecure setups behind an unactionable toast.
		// dialOpts stays empty → process-wide insecure callback is used.
	case store != nil:
		dialOpts.HostKeyCallback = store.CaptureCallback(func(ck hostkeys.CapturedKey) {
			capturedFP = ck.Fingerprint
			capturedPrev = ck.Previous
			capturedChanged = ck.Changed
		})
	case khPath != "":
		cb, cberr := sftpx.CapturePreviewCallback(khPath, func(ck sftpx.CapturedKey) {
			capturedFP = ck.Fingerprint
			capturedPrev = ck.Previous
			capturedChanged = ck.Changed
		})
		if cberr != nil {
			out["stage"] = "ssh_or_sftp"
			out["error"] = "host-key check setup: " + cberr.Error()
			writeJSON(w, out)
			return
		}
		dialOpts.HostKeyCallback = cb
	}
	// Public-key auth: if the caller supplied a PEM, parse it now and use
	// it instead of the password. A parse failure short-circuits the probe
	// with a clean error so the operator sees the issue without it being
	// shadowed by a downstream "auth failed" from the SSH layer.
	if req.PrivateKey != "" {
		signer, perr := sftpx.ParsePrivateKey([]byte(req.PrivateKey), req.Passphrase)
		if perr != nil {
			out["stage"] = "ssh_or_sftp"
			out["error"] = "private key: " + perr.Error()
			writeJSON(w, out)
			return
		}
		dialOpts.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	}
	c, err := sftpx.DialWithOpts(req.Host, req.Port, req.Username, req.Password, dialOpts)
	if err != nil {
		log.Printf("probe %s user=%s: ssh: %v", addr, req.Username, err)
		out["stage"] = "ssh_or_sftp"
		out["ssh_ms"] = time.Since(t1).Milliseconds()
		// If the failure was specifically the "user consent required" sentinel
		// from CapturePreviewCallback AND we successfully captured a
		// fingerprint, surface it so the UI can render an Accept/Reject
		// prompt. Same shape regardless of whether the SSH error wraps the
		// sentinel directly or in a transport-layer message.
		switch {
		case capturedChanged && capturedFP != "":
			// Renewed / rotated / possibly-MITM. UI must show a high-friction
			// prompt with both fingerprints before the operator opts in.
			out["requires_renewal"] = true
			out["captured_fingerprint"] = capturedFP
			out["captured_previous_fingerprint"] = capturedPrev
			out["captured_for_host"] = req.Host
			out["error"] = "Server presented a DIFFERENT host key than the one already in known_hosts. Verify out-of-band before accepting."
		case capturedFP != "" && (errors.Is(err, sftpx.ErrHostKeyConsentRequired) ||
			errors.Is(err, hostkeys.ErrUnknownHost) ||
			strings.Contains(err.Error(), "user consent required") ||
			strings.Contains(err.Error(), "knownhosts: key is unknown") ||
			strings.Contains(err.Error(), "host key not trusted") ||
			strings.Contains(err.Error(), "ssh: handshake failed")):
			out["requires_consent"] = true
			out["captured_fingerprint"] = capturedFP
			out["captured_for_host"] = req.Host
			out["error"] = "Server presented a new host key. Verify the fingerprint and accept to continue."
		default:
			out["error"] = friendlyProbeError("ssh_or_sftp", err)
		}
		writeJSON(w, out)
		return
	}
	out["ssh_sftp_ms"] = time.Since(t1).Milliseconds()
	defer c.Close()
	if capturedFP != "" && req.TrustOnFirstUse {
		out["captured_fingerprint"] = capturedFP
		out["captured_for_host"] = req.Host
		// Store mode: the in-memory map already has the new key — the
		// process-wide StrictCallback reads it on every Dial, no reload
		// needed. File mode: the process-wide callback was loaded once
		// at startup from the OpenSSH file; reload it so next /api/start
		// sees the freshly-appended entry.
		if store == nil {
			if khPath := s.getKnownHostsPath(); khPath != "" {
				if err := sftpx.UseKnownHosts(khPath); err != nil {
					log.Printf("reload known_hosts after TOFU: %v", err)
				}
			}
		}
	}

	// Stage 4 — optional folder list. Validates the remote path exists +
	// the user can read it. Common configuration mistake to catch early.
	if req.Folder != "" {
		t2 := time.Now()
		_, err := c.List(req.Folder)
		out["list_ms"] = time.Since(t2).Milliseconds()
		if err != nil {
			log.Printf("probe %s user=%s: list %q: %v", addr, req.Username, req.Folder, err)
			out["stage"] = "list"
			out["error"] = friendlyProbeError("list", err)
			writeJSON(w, out)
			return
		}
	}

	out["ok"] = true
	out["stage"] = "complete"
	writeJSON(w, out)
}

// probeFTP runs an FTP / FTPS connect+login+optional-list against the
// supplied target. Same JSON shape as the SFTP path: per-stage timings
// (connect_ms / list_ms), error message, plus the captured TLS leaf-cert
// fingerprint when the protocol is FTPS so the UI can drive cert TOFU.
//
// Cert TOFU mirrors the SSH host-key flow:
//   - cert unknown to the store + tofu=false   → requires_consent=true
//   - cert known but DIFFERENT + tofu=false    → requires_renewal=true
//   - cert known + matches                     → silent ok
//   - tofu=true (operator opted in)            → silent OK on first cert,
//                                                 store records it
//   - acceptChanged=true + tofu=true           → drop existing entry
//                                                 first, then record new
func (s *Server) probeFTP(w http.ResponseWriter, out map[string]any, proto protocol.Protocol, host string, port int, user, pass, folder, tlsMode string, insecureSkipVerify bool, tlsServerName string, tofu, acceptChanged bool) {
	if user == "" {
		out["ok"] = true
		out["stage"] = "tcp"
		out["note"] = "TCP only — supply username + password to verify FTP login"
		writeJSON(w, out)
		return
	}
	tlsStore := s.getTLSStore()
	// AcceptChanged: drop the existing trust entry so the next dial sees
	// a clean slate and records the newly-presented cert.
	if proto == protocol.FTPS && acceptChanged && tofu && tlsStore != nil {
		if _, rerr := tlsStore.Remove(host, port); rerr != nil {
			log.Printf("probe ftps://%s:%d: remove existing tls trust entry: %v", host, port, rerr)
			out["stage"] = "connect"
			out["error"] = "could not update trust store to overwrite previous cert"
			writeJSON(w, out)
			return
		}
		log.Printf("probe ftps://%s:%d: removed previous tls trust entry per accept_changed", host, port)
	}
	t1 := time.Now()
	var capturedFP string
	dialOpts := protocol.DialOpts{
		Host:               host,
		Port:               port,
		User:               user,
		Pass:               pass,
		TLSMode:            protocol.ParseTLSMode(tlsMode),
		InsecureSkipVerify: insecureSkipVerify,
		TLSServerName:      tlsServerName,
		TLSCaptureCallback: func(fp string) { capturedFP = fp },
	}
	// FTPS attaches the TLS trust store unless the operator explicitly
	// ticked "Skip TLS verification" — that's a "trust any cert" gate
	// weaker than the store, so the store has no role there. The TOFU
	// flag rides on TLSTrustOnFirstUse so the same store-backed
	// VerifyConnection path the runner uses (added in v0.13.8) drives
	// the probe too. Without this wiring, probe-with-TOFU dialed with
	// the system CA chain → rejected the self-signed lab cert →
	// surfaced as a generic handshake error before the post-success
	// Add block could run.
	if proto == protocol.FTPS && !insecureSkipVerify && tlsStore != nil {
		dialOpts.TLSStore = tlsStore
		dialOpts.TLSTrustOnFirstUse = tofu
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := protocol.Dial(ctx, proto, dialOpts)
	out["connect_ms"] = time.Since(t1).Milliseconds()
	// Surface ssh_sftp_ms too so the legacy stages widget renders something
	// meaningful even when the operator doesn't switch labels in the UI.
	out["ssh_sftp_ms"] = out["connect_ms"]
	if err != nil {
		log.Printf("probe %s://%s:%d user=%s: %v", proto, host, port, user, err)
		out["stage"] = "connect"
		// Surface a structured cert-consent / cert-renewal payload when
		// the failure is a TLSVerifyError, so the UI can drive the same
		// hostKeyConsent modal it already shows for SSH.
		var verr *hostkeys.TLSVerifyError
		if proto == protocol.FTPS && errors.As(err, &verr) {
			out["captured_fingerprint"] = verr.Fingerprint
			out["captured_for_host"] = host
			out["tls_fingerprint"] = verr.Fingerprint
			if errors.Is(verr.Err, hostkeys.ErrTLSCertChanged) {
				out["requires_renewal"] = true
				out["captured_previous_fingerprint"] = verr.Previous
				out["error"] = "FTPS server presented a DIFFERENT certificate than the one already trusted. Verify out-of-band before accepting."
			} else { // ErrUnknownTLSHost
				out["requires_consent"] = true
				out["error"] = "FTPS server presented a new certificate. Verify the fingerprint and accept to continue."
			}
			writeJSON(w, out)
			return
		}
		out["error"] = friendlyFTPError(proto, err)
		if capturedFP != "" {
			out["captured_fingerprint"] = capturedFP
			out["captured_for_host"] = host
			out["tls_fingerprint"] = capturedFP
		}
		writeJSON(w, out)
		return
	}
	defer c.Close()
	if proto == protocol.FTPS {
		var fp string
		var leaf *x509.Certificate
		if cert := protocol.TLSPeerCertificate(c); cert != nil {
			leaf = cert
			fp = protocol.Fingerprint(cert)
		} else if capturedFP != "" {
			fp = capturedFP
		}
		if fp != "" {
			out["captured_fingerprint"] = fp
			out["captured_for_host"] = host
			out["tls_fingerprint"] = fp
			// TOFU=true with a fresh successful Dial → record the cert so
			// next probe verifies against it. Mirrors the SFTP TOFU path.
			if tofu && tlsStore != nil && leaf != nil {
				if aerr := tlsStore.Add(host, port, leaf); aerr != nil {
					log.Printf("probe ftps://%s:%d: store add: %v", host, port, aerr)
				}
			}
		}
	}
	if folder != "" {
		t2 := time.Now()
		_, lerr := c.List(folder)
		out["list_ms"] = time.Since(t2).Milliseconds()
		if lerr != nil {
			log.Printf("probe %s://%s:%d list %q: %v", proto, host, port, folder, lerr)
			out["stage"] = "list"
			out["error"] = friendlyProbeError("list", lerr)
			writeJSON(w, out)
			return
		}
	}
	out["ok"] = true
	out["stage"] = "complete"
	writeJSON(w, out)
}

// /api/host — one-shot snapshot of the client machine's capacity. Called
// once at UI load (and any time the operator wants to refresh) so testers
// always see the real ceilings (FD limit, cores, RAM, NICs) of the box
// they're running on. Cheap — no subprocess, no probes; just rlimit +
// /dev/fd readdir + /proc reads.
func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, hostinfo.Snapshot())
}

// handleHealth answers liveness probes. Returns minimal {"status":"ok"} by
// default so the unauthenticated endpoint cannot be used to fingerprint
// uptime or detect when a run is active. When ?detail=1 is passed the
// BasicAuth middleware enforces credentials before this handler runs (see
// security.go), so reaching here with detail=1 means the caller is
// authenticated (or auth is disabled by config).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"status": "ok"}
	if r.URL.Query().Get("detail") == "1" {
		active := s.activeRun()
		out["uptime_sec"] = int64(time.Since(processStart).Seconds())
		out["active_run"] = active != nil
		if active != nil {
			out["active_run_id"] = active.ID
		}
		if v := s.getVersion(); v != "" {
			out["version"] = v
		}
	}
	writeJSON(w, out)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	// Wrap the static file server so every asset response carries
	// Cache-Control: no-store. The Wails desktop app reuses the same
	// embedded HTML / CSS / JS, and WebKit on macOS persists a per-app
	// cache under ~/Library/WebKit/<bundle-id>/ that survives across
	// app launches AND across .app rebuilds. Without an explicit
	// no-store, an upgraded binary's fresh JS is silently shadowed by
	// stale cached bytes for hours — visible symptom is the run form
	// using the previous build's serializer (e.g. shipping
	// tls_trust_on_first_use:false even though the new HTML defaults
	// the toggle on). We pay one extra fetch per page load in exchange
	// for parity with the binary that's running.
	mux.Handle("/", noStoreAssets(http.FileServer(http.FS(sub))))

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/host", s.handleHost)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/report.csv", s.handleReportCSV)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/schedule", s.handleScheduleCreate)
	mux.HandleFunc("/api/schedules", s.handleScheduleList)
	mux.HandleFunc("/api/schedule/cancel", s.handleScheduleCancel)
	mux.HandleFunc("/api/hostkeys", s.handleHostKeys)
	mux.HandleFunc("/api/hostkeys/remove", s.handleHostKeysRemove)
	mux.HandleFunc("/api/cluster/start", s.handleClusterStart)
	mux.HandleFunc("/api/cluster/status", s.handleClusterStatus)
	mux.HandleFunc("/api/cluster/stop", s.handleClusterStop)
	mux.HandleFunc("/api/cluster/runs", s.handleClusterRuns)
	mux.HandleFunc("/api/cluster/runs/file", s.handleClusterRunFile)
	mux.HandleFunc("/api/worker/spawn", s.handleWorkerSpawn)
	mux.HandleFunc("/api/worker/despawn", s.handleWorkerDespawn)
	mux.HandleFunc("/api/worker/spawned", s.handleWorkerSpawnedList)
	mux.HandleFunc("/api/worker/preflight", s.handleWorkerPreflight)
	mux.HandleFunc("/api/worker/probe", s.handleWorkerProbe)
	return mux
}

// handleHostKeys returns the list of trusted host keys. Read-only; the UI
// renders a row per entry with a delete button that hits /api/hostkeys/remove.
// In legacy file-mode we surface a small {mode:"file"} stub so the UI can
// fall back to "managed externally — see -known-hosts file" instead of
// pretending the list is empty.
func (s *Server) handleHostKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	store := s.getHostKeyStore()
	if store == nil {
		writeJSON(w, map[string]any{
			"mode":  "file",
			"path":  s.getKnownHostsPath(),
			"hosts": []any{},
		})
		return
	}
	writeJSON(w, map[string]any{
		"mode":  "store",
		"path":  store.Path(),
		"hosts": store.List(),
	})
}

// handleHostKeysRemove deletes a single trust entry. Body: {host, port}.
// Only meaningful in store-mode; in file-mode the operator owns the file.
func (s *Server) handleHostKeysRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	store := s.getHostKeyStore()
	if store == nil {
		http.Error(w, "trust store not enabled (file-mode); edit the known_hosts file directly", http.StatusBadRequest)
		return
	}
	var req struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host required", http.StatusBadRequest)
		return
	}
	removed, err := store.Remove(req.Host, req.Port)
	if err != nil {
		http.Error(w, "remove: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"removed": removed})
}

// latest returns the most recently started run, or nil if none.
func (s *Server) latest() *runner.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return nil
	}
	return s.runs[s.order[len(s.order)-1]]
}

// pick resolves a run by the `run` query param, or falls back to the latest.
func (s *Server) pick(r *http.Request) *runner.Run {
	if id := r.URL.Query().Get("run"); id != "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.runs[id]
	}
	return s.latest()
}

// activeRun returns the currently-running run, if any.
func (s *Server) activeRun() *runner.Run {
	run := s.latest()
	if run == nil {
		return nil
	}
	if run.IsActive() {
		return run
	}
	return nil
}

type startReq struct {
	Host                   string  `json:"host"`
	Port                   int     `json:"port"`
	UploadFolder           string  `json:"upload_folder"`
	ParallelStreams        int     `json:"parallel_streams"`
	DurationHours          float64 `json:"duration_hours"`
	PollSeconds            int     `json:"poll_seconds"`
	TrackIDTimeoutS        int     `json:"track_id_timeout_seconds"`
	MaxConsecutiveFailures int     `json:"max_consecutive_failures"`

	// Protocol selects sftp / ftp / ftps. Empty = sftp (back-compat with
	// configs saved before v0.13.0).
	Protocol              string `json:"protocol,omitempty"`
	TLSMode               string `json:"tls_mode,omitempty"`
	TLSInsecureSkipVerify bool   `json:"tls_insecure_skip_verify,omitempty"`
	TLSServerName         string `json:"tls_server_name,omitempty"`
	// TLSTrustOnFirstUse mirrors the SFTP host-key TOFU semantics for
	// FTPS leaf certs: when true, the runner adds an unknown server's
	// cert to the persistent trust store on first contact instead of
	// failing the run. False = unknown certs refuse (operator must
	// probe-and-accept first). Has no effect when TLSInsecureSkipVerify
	// is set; that flag bypasses the store entirely.
	TLSTrustOnFirstUse    bool   `json:"tls_trust_on_first_use,omitempty"`

	NormalEnabled     bool   `json:"normal_enabled"`
	FilesPerMinute    int    `json:"files_per_minute"`
	NormalMinMB       int    `json:"normal_min_mb"`
	NormalMaxMB       int    `json:"normal_max_mb"`
	NormalContentType string `json:"normal_content_type"`
	NormalUsersCSV    string `json:"normal_users_csv"`

	LargeEnabled    bool   `json:"large_enabled"`
	LargeMin        int    `json:"large_min"`
	LargeMax        int    `json:"large_max"`
	LargeUnit       string `json:"large_unit"` // "MB" or "GB"
	IntervalMinutes int    `json:"interval_minutes"`
	LargeUsersCSV   string `json:"large_users_csv"`

	DownloadEnabled         bool   `json:"download_enabled"`
	DownloadFolder          string `json:"download_folder"`
	DownloadParallelStreams int    `json:"download_parallel_streams"`
	// DownloadMatchMode picks how outbox files pair back to uploads:
	// "trackid" (default; server adds "#<id>" suffix) or "filename"
	// (server preserves a marker the runner injects into the upload).
	DownloadMatchMode string `json:"download_match_mode"`
	DownloadUsersCSV        string `json:"download_users_csv"`

	// PrivateKeyPEM, when non-empty, switches the run from password to
	// public-key auth (v1: shared across all SFTP users in the run).
	// PrivateKeyPassphrase decrypts encrypted PEMs.
	PrivateKeyPEM        string `json:"private_key_pem"`
	PrivateKeyPassphrase string `json:"private_key_passphrase"`

	// v0.14 — per-load source / sink overrides. The structs are defined
	// in internal/config and accept JSON-passed-through verbatim. nil
	// in any of them keeps the v0.13.x defaults (synthetic upload bytes
	// + io.Discard download bytes).
	NormalSource    *config.SourceConfig `json:"normal_source,omitempty"`
	LargeSource     *config.SourceConfig `json:"large_source,omitempty"`
	DownloadSink    *config.SinkConfig   `json:"download_sink,omitempty"`
}

// buildRunConfig converts a startReq to a RunConfig, parsing the embedded
// user CSVs. Returns a friendly error suitable for 4xx responses.
func buildRunConfig(req startReq) (*config.RunConfig, error) {
	cfg := &config.RunConfig{
		Host:                   req.Host,
		Port:                   req.Port,
		UploadFolder:           req.UploadFolder,
		ParallelStreams:        req.ParallelStreams,
		DurationHours:          req.DurationHours,
		PollInterval:           time.Duration(req.PollSeconds) * time.Second,
		TrackIDTimeout:         time.Duration(req.TrackIDTimeoutS) * time.Second,
		MaxConsecutiveFailures: req.MaxConsecutiveFailures,
		PrivateKeyPEM:          req.PrivateKeyPEM,
		PrivateKeyPassphrase:   req.PrivateKeyPassphrase,
		Protocol:               req.Protocol,
		TLSMode:                req.TLSMode,
		TLSInsecureSkipVerify:  req.TLSInsecureSkipVerify,
		TLSServerName:          req.TLSServerName,
		TLSTrustOnFirstUse:     req.TLSTrustOnFirstUse,
	}
	if req.NormalEnabled {
		cfg.Normal = &config.NormalLoad{
			FilesPerMinute: req.FilesPerMinute,
			MinSizeMB:      req.NormalMinMB,
			MaxSizeMB:      req.NormalMaxMB,
			ContentType:    req.NormalContentType,
			Source:         req.NormalSource, // nil-safe; keeps synthetic default
		}
		users, err := config.ParseUsersCSV(strings.NewReader(req.NormalUsersCSV))
		if err != nil {
			return nil, fmt.Errorf("normal users csv: %w", err)
		}
		cfg.NormalUsers = users
	}
	if req.LargeEnabled {
		cfg.LargeFile = &config.LargeFileLoad{
			MinSize:         req.LargeMin,
			MaxSize:         req.LargeMax,
			Unit:            req.LargeUnit,
			IntervalMinutes: req.IntervalMinutes,
			Source:          req.LargeSource,
		}
		users, err := config.ParseUsersCSV(strings.NewReader(req.LargeUsersCSV))
		if err != nil {
			return nil, fmt.Errorf("large users csv: %w", err)
		}
		cfg.LargeFileUsers = users
	}
	if req.DownloadEnabled {
		cfg.Download = &config.DownloadLoad{
			Folder:          req.DownloadFolder,
			ParallelStreams: req.DownloadParallelStreams,
			MatchMode:       req.DownloadMatchMode,
			Sink:            req.DownloadSink,
		}
		users, err := config.ParseUsersCSV(strings.NewReader(req.DownloadUsersCSV))
		if err != nil {
			return nil, fmt.Errorf("download users csv: %w", err)
		}
		cfg.DownloadUsers = users
	}
	return cfg, nil
}

// startRun is the single code path that creates a Run from a startReq and
// registers it in the server's in-memory map. Both /api/start and the
// scheduler go through here so they stay in lockstep. startedBy tags the
// run ("manual" or "schedule") so the UI can badge it.
func (s *Server) startRun(req startReq, startedBy string) (*runner.Run, error) {
	if s.activeRun() != nil {
		return nil, fmt.Errorf("a run is already active — stop it first")
	}
	cfg, err := buildRunConfig(req)
	if err != nil {
		return nil, err
	}
	run, err := runner.StartWithPersistAndTLS(context.Background(), cfg, s.reportsDir, s.getTLSStore())
	if err != nil {
		return nil, err
	}
	run.StartedBy = startedBy
	s.mu.Lock()
	s.runs[run.ID] = run
	s.order = append(s.order, run.ID)
	for len(s.order) > maxRetainedRuns {
		oldID := s.order[0]
		old := s.runs[oldID]
		if old != nil && old.IsActive() {
			break
		}
		delete(s.runs, oldID)
		s.order = s.order[1:]
	}
	s.mu.Unlock()
	return run, nil
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req startReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Pre-flight host-key check. Without this, a Start Run that hits an
	// unknown or changed key only surfaces as per-record errors after the
	// run starts streaming — operators have no remediation path. Try a
	// single dial with the first available user via the capture callback;
	// if consent is needed, return the structured response so the UI can
	// prompt and re-call /api/start once the operator accepts.
	//
	// SFTP-only path: FTP and FTPS don't use SSH host keys. FTPS cert TOFU
	// is exercised by the probe handler before the operator hits Start Run;
	// re-checking it here would only add a redundant dial.
	preflightProto := protocol.Normalize(req.Protocol)
	if preflightProto == protocol.SFTP && (s.getHostKeyStore() != nil || s.getKnownHostsPath() != "") {
		creds := firstStartCredential(req)
		if creds.user != "" && req.Host != "" && req.Port > 0 {
			// When the run is configured with a shared private key, the
			// preflight dial must use it too — a password preflight against
			// a key-only server would surface auth failures that the actual
			// run wouldn't hit. We still preflight so host-key consent is
			// exercised, but with the same auth method the run will use.
			var preAuth []ssh.AuthMethod
			if req.PrivateKeyPEM != "" {
				if signer, perr := sftpx.ParsePrivateKey([]byte(req.PrivateKeyPEM), req.PrivateKeyPassphrase); perr == nil {
					preAuth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
				}
			}
			if pre := s.preflightHostKey(req.Host, req.Port, creds.user, creds.pass, preAuth); pre != nil {
				writeJSON(w, pre)
				return
			}
		}
	}

	run, err := s.startRun(req, "manual")
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "already active") {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]any{"run_id": run.ID})
}

// preflightHostKey runs a single capture-callback dial. Returns a non-nil
// JSON payload only when the UI needs to show a consent prompt — unknown
// host (requires_consent) or changed host key (requires_renewal). All other
// errors (TCP refused, auth failure, etc.) are tolerated here so the actual
// run can start and propagate them as ordinary per-file failures.
//
// Mode selection mirrors handleProbe: store-mode when SetHostKeyStore was
// called, file-mode otherwise.
func (s *Server) preflightHostKey(host string, port int, user, pass string, auth []ssh.AuthMethod) map[string]any {
	var capturedFP, capturedPrev string
	var capturedChanged bool
	var dialOpts sftpx.DialOpts
	dialOpts.Auth = auth
	if store := s.getHostKeyStore(); store != nil {
		dialOpts.HostKeyCallback = store.CaptureCallback(func(ck hostkeys.CapturedKey) {
			capturedFP = ck.Fingerprint
			capturedPrev = ck.Previous
			capturedChanged = ck.Changed
		})
	} else if khPath := s.getKnownHostsPath(); khPath != "" {
		cb, cberr := sftpx.CapturePreviewCallback(khPath, func(ck sftpx.CapturedKey) {
			capturedFP = ck.Fingerprint
			capturedPrev = ck.Previous
			capturedChanged = ck.Changed
		})
		if cberr != nil {
			log.Printf("preflight setup: %v", cberr)
			return nil
		}
		dialOpts.HostKeyCallback = cb
	} else {
		return nil
	}
	c, err := sftpx.DialWithOpts(host, port, user, pass, dialOpts)
	if err == nil {
		c.Close()
		return nil
	}
	switch {
	case capturedChanged && capturedFP != "":
		log.Printf("start preflight %s:%d: host key changed (was %s, now %s)", host, port, capturedPrev, capturedFP)
		return map[string]any{
			"ok":                              false,
			"requires_renewal":                true,
			"captured_fingerprint":            capturedFP,
			"captured_previous_fingerprint":   capturedPrev,
			"captured_for_host":               host,
			"error":                           "Server presented a DIFFERENT host key than the one already in known_hosts. Verify out-of-band before accepting.",
		}
	case capturedFP != "" && (errors.Is(err, sftpx.ErrHostKeyConsentRequired) ||
		errors.Is(err, hostkeys.ErrUnknownHost) ||
		strings.Contains(err.Error(), "user consent required") ||
		strings.Contains(err.Error(), "knownhosts: key is unknown") ||
		strings.Contains(err.Error(), "host key not trusted")):
		log.Printf("start preflight %s:%d: unknown host key (%s)", host, port, capturedFP)
		return map[string]any{
			"ok":                   false,
			"requires_consent":     true,
			"captured_fingerprint": capturedFP,
			"captured_for_host":    host,
			"error":                "Server presented a new host key. Verify the fingerprint and accept to continue.",
		}
	}
	// Other dial errors fall through — let the actual run start so the
	// failure shows up in the records / disabled-users surfaces.
	return nil
}

type startCred struct{ user, pass string }

// firstStartCredential picks the first usable credential from the run config:
// normal users, then large-file users, then download users. Used solely for the
// pre-flight host-key dial so any one user is enough to surface a key issue.
func firstStartCredential(req startReq) startCred {
	parse := func(csv string) startCred {
		for _, line := range strings.Split(csv, "\n") {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			parts := strings.Split(t, ",")
			if len(parts) >= 2 {
				return startCred{user: strings.TrimSpace(parts[0]), pass: parts[1]}
			}
		}
		return startCred{}
	}
	for _, csv := range []string{req.NormalUsersCSV, req.LargeUsersCSV, req.DownloadUsersCSV} {
		if c := parse(csv); c.user != "" {
			return c
		}
	}
	return startCred{}
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	run := s.activeRun()
	if run == nil {
		http.Error(w, "no active run", http.StatusNotFound)
		return
	}
	run.Stop()
	writeJSON(w, map[string]any{"stopped": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	run := s.pick(r)
	procStats := s.procMon.Sample()
	if run == nil {
		// If the caller asked for a specific run that only lives on disk,
		// surface its metadata so the UI can still show totals alongside the
		// CSV link. Otherwise return the "no active run" shape.
		if id := r.URL.Query().Get("run"); id != "" && s.reportsDir != "" {
			if metas, err := persist.ListMeta(s.reportsDir); err == nil {
				for _, m := range metas {
					if m.ID == id {
						writeJSON(w, map[string]any{
							"active":     false,
							"run_id":     m.ID,
							"started_at": m.StartedAt,
							"historical": true,
							"metrics": map[string]any{
								"total_files":  m.TotalFiles,
								"total_bytes":  m.TotalBytes,
								"overall_mbps": m.OverallMBps,
							},
							"proc": procStats,
						})
						return
					}
				}
			}
		}
		writeJSON(w, map[string]any{"active": false, "proc": procStats})
		return
	}
	active := run.IsActive()
	// Tail snapshot only — the status poll returns at most the last 200 rows,
	// so copying the full in-memory slice every 2s (O(total)) was wasted work
	// on long runs. SnapshotTail is O(200) regardless of run length.
	recs := run.Report.SnapshotTail(200)
	writeJSON(w, map[string]any{
		"active":              active,
		"run_id":              run.ID,
		"started_at":          run.StartedAt,
		"started_by":          run.StartedBy,
		"metrics":             run.Metrics.Snapshot(),
		"slowdowns_enriched":  run.EnrichSlowdowns(),
		"records":             recs,
		"pending_trackids":    run.Watcher.PendingCount(),
		"dispatch_skips":      run.DispatchSkips.Load(),
		"download_completed":  run.DownloadCompleted.Load(),
		"download_orphans":    run.DownloadOrphans.Load(),
		"download_in_queue":   run.DownloadQueueDepth(), // 0 under pairless poll model
		"download_dropped":    run.DownloadDropped.Load(),
		// Workload-shape flags so the live UI can hide tiles that
		// don't apply to the active run (download tiles, large-file
		// stats, etc.). Without these the operator sees a 0-valued
		// tile and wonders whether the feature failed silently.
		"download_enabled":    run.Cfg != nil && run.Cfg.Download != nil,
		"normal_enabled":      run.Cfg != nil && run.Cfg.Normal != nil,
		"large_enabled":       run.Cfg != nil && run.Cfg.LargeFile != nil,
		"failed_files":        run.FailedFiles.Load(),
		"failed_bytes":        run.FailedBytes.Load(),
		"errors_by_code":      run.ErrorsByCode(),
		"disabled_users":      run.DisabledUsers(),
		"records_in_memory":   run.Report.LiveCount(),
		"records_flushed":     run.Report.FlushedCount(),
		"proc":                procStats,
		"latency": map[string]any{
			"upload":     latencyStageJSON(run.UploadLatency.Snapshot()),
			"upload_cor": latencyStageJSON(run.UploadLatencyCOR.Snapshot()),
			"dial":       latencyStageJSON(run.DialLatency.Snapshot()),
		},
	})
}

// latencyStageJSON renders a histogram snapshot in the same shape the
// persisted RunMeta uses, so live (/api/status) and historical
// (/api/runs) consumers see the same field names.
func latencyStageJSON(s latency.Snapshot) map[string]any {
	if s.Count == 0 {
		return nil
	}
	return map[string]any{
		"count":    s.Count,
		"p50_ns":   s.P50,
		"p95_ns":   s.P95,
		"p99_ns":   s.P99,
		"p999_ns":  s.P999,
		"max_ns":   s.Max,
		"mean_ns":  s.Mean,
	}
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	// Live / in-memory runs.
	s.mu.Lock()
	live := make([]map[string]any, 0, len(s.order))
	liveIDs := map[string]bool{}
	for _, id := range s.order {
		run, ok := s.runs[id]
		if !ok {
			continue
		}
		liveIDs[id] = true
		snap := run.Metrics.Snapshot()
		// If a run is no longer active AND its meta JSON has already
		// been sealed to disk, prefer the historical entry: it carries
		// the full analyzer narrative (suggestions, infra peaks,
		// latency) the live state hasn't computed yet. The live entry's
		// only value during a finished-but-still-in-memory run is
		// freshness, and the seal has already produced the finalised
		// values.
		if !run.IsActive() && s.reportsDir != "" {
			if hist := historicalForLiveID(s.reportsDir, run.ID); hist != nil {
				live = append(live, runMetaToMap(*hist, false, "memory+disk"))
				continue
			}
		}
		// Enrich the live entry with the same field set the historical
		// (post-seal) entry carries, so a just-completed run shown in
		// the Previous-runs card has its full analysis (latency, infra
		// peaks, dispatch skips) rather than looking like a stub until
		// the in-memory entry is eventually evicted. Source-of-truth
		// for these is whatever the live Run currently holds.
		entry := map[string]any{
			"id":           run.ID,
			"started_at":   run.StartedAt,
			"started_by":   run.StartedBy,
			"active":       run.IsActive(),
			"total_files":  snap.TotalFiles,
			"total_bytes":  snap.TotalBytes,
			"overall_mbps": snap.OverallMBps,
			"failed_files": run.FailedFiles.Load(),
			"dispatch_skips": run.DispatchSkips.Load(),
			"source":       "memory",
		}
		if run.Cfg != nil {
			entry["upload_users"] = len(run.Cfg.NormalUsers)
			entry["parallel_streams"] = run.Cfg.ParallelStreams
			if run.Cfg.Normal != nil {
				entry["files_per_minute"] = run.Cfg.Normal.FilesPerMinute
			}
			if run.Cfg.Download != nil {
				entry["download_enabled"] = true
				entry["download_users"] = len(run.Cfg.DownloadUsers)
				entry["download_parallel_streams"] = run.Cfg.Download.ParallelStreams
				if run.Cfg.Download.MatchMode != "" {
					entry["download_match_mode"] = run.Cfg.Download.MatchMode
				}
			}
		}
		// Latency: snapshot the live histograms so percentile points
		// appear immediately on the card, not just after the seal.
		entry["latency"] = map[string]any{
			"upload":     latencyStageJSON(run.UploadLatency.Snapshot()),
			"upload_cor": latencyStageJSON(run.UploadLatencyCOR.Snapshot()),
			"dial":       latencyStageJSON(run.DialLatency.Snapshot()),
		}
		live = append(live, entry)
	}
	s.mu.Unlock()
	// Historical runs from disk (skip any that are already in-memory).
	var historical []map[string]any
	if s.reportsDir != "" {
		metas, _ := persist.ListMeta(s.reportsDir)
		for _, m := range metas {
			if liveIDs[m.ID] {
				continue
			}
			historical = append(historical, runMetaToMap(m, false, "disk"))
		}
	}
	// Newest first: reverse live, append historical (already sorted newest-first).
	for i, j := 0, len(live)-1; i < j; i, j = i+1, j-1 {
		live[i], live[j] = live[j], live[i]
	}
	writeJSON(w, map[string]any{"runs": append(live, historical...)})
}

func (s *Server) handleReportCSV(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run")
	run := s.pick(r)
	// Live / in-memory run: serve the streaming CSV file (rows already
	// sealed) + the in-memory tail (rows still mutable). This keeps the
	// download correct even on multi-hour runs where the in-memory slice
	// alone would miss most of the data.
	if run != nil {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, run.ID))
		snap := run.Metrics.Snapshot()
		eff := func(bytes int64, startTime time.Time, dur time.Duration) float64 {
			return runner.EffectiveSpeedMBps(bytes, startTime, dur, snap)
		}
		streamPath := run.ReportStreamPath()
		if streamPath != "" {
			// Active streaming path: the on-disk file is append-only. Read its
			// current size, stream those bytes (frozen — flushes only ever
			// extend past this mark), then append the in-memory tail as CSV
			// rows. If the file is empty yet (no row has been flushed), write
			// the header ourselves so clients always get a valid file.
			if data, err := os.ReadFile(streamPath); err == nil && len(data) > 0 {
				_, _ = w.Write(data)
			} else {
				cw := csv.NewWriter(w)
				_ = cw.Write(report.CSVHeader)
				cw.Flush()
			}
			cw := csv.NewWriter(w)
			_ = run.Report.WriteRemainingCSV(cw, run.SlowdownMinutes(), eff)
			cw.Flush()
			return
		}
		// Run is in-memory but its stream writer has been closed (finalized).
		// The fully-flushed file lives on disk; stream that. We only fall back
		// to the in-memory snapshot when no reports-dir is configured at all
		// (and even then, after seal, the snapshot is typically drained).
		if s.reportsDir != "" {
			if f, err := os.Open(persist.CSVPath(s.reportsDir, run.ID)); err == nil {
				defer f.Close()
				_, _ = io.Copy(w, f)
				return
			}
		}
		if err := report.WriteCSV(w, run.Report.Snapshot(), run.SlowdownMinutes(), eff); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	// Historical run → stream the file from disk.
	if runID == "" || s.reportsDir == "" {
		http.Error(w, "no run", http.StatusNotFound)
		return
	}
	path := persist.CSVPath(s.reportsDir, runID)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "report not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, runID))
	// Stream the file contents; client disconnects are swallowed as usual.
	_, _ = io.Copy(w, f)
}

// friendlyProbeError translates raw SSH/SFTP/TCP error strings into a stable,
// user-facing message. The original error is logged server-side via
// log.Printf at the call site so operators can still debug, but the JSON
// response no longer leaks library-specific stderr (handshake fingerprints,
// SSH protocol versions, file paths in known_hosts wrapping). Stage is one
// of "tcp", "ssh_or_sftp", "list".
//
// SFTP-specific. FTP/FTPS probes use friendlyFTPError instead — that
// helper maps TLS-handshake / FTP-control-channel patterns to FTP
// language so the operator never sees "SSH handshake failed" on a
// run targeting an FTPS server.
func friendlyProbeError(stage string, err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "no such host"):
		return "Hostname could not be resolved."
	case strings.Contains(low, "connection refused"):
		return "Connection refused — verify the SFTP server is running on this port."
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "deadline exceeded"):
		return "Connection timed out — verify the host is reachable and not firewalled."
	case strings.Contains(low, "network is unreachable"):
		return "Network is unreachable from this host."
	case strings.Contains(low, "host key has changed"), strings.Contains(low, "possible mitm"):
		// This branch is a fallback — the structured requires_renewal path
		// (above the friendlyProbeError call) handles the normal flow with
		// both fingerprints in the JSON response. Reaching here means the
		// callback didn't capture the changed key for some reason; tell
		// the operator to retry from the UI instead of editing files.
		return "Host key mismatch detected. Open Test Connection to view both fingerprints and decide whether to trust the new key."
	case strings.Contains(low, "knownhosts: key is unknown"), strings.Contains(low, "user consent required"):
		return "Server presented a new host key. Verify the fingerprint and accept to continue."
	case strings.Contains(low, "unable to authenticate"), strings.Contains(low, "authentication failed"):
		return "Authentication failed — verify username and password."
	// FTPS-shaped TLS errors hitting the SFTP path mean the operator
	// pointed an SFTP probe at a TLS-fronted port. Steer them to the
	// right protocol picker rather than the misleading "SSH handshake
	// failed" message — the SSH layer never even started.
	case strings.Contains(low, "tls"), strings.Contains(low, "x509"):
		return "Server speaks TLS, not SSH — switch the protocol to FTPS (or FTP) in the connection form, or point this probe at port 22."
	case strings.Contains(low, "ssh: handshake failed"):
		return "SSH handshake failed — check server config, credentials, or network. (If the target is FTPS or FTP, switch the protocol in the connection form.)"
	case strings.Contains(low, "subsystem"):
		return "Server accepted SSH but rejected the SFTP subsystem — verify SFTP is enabled."
	case stage == "list":
		return "Folder listing failed — verify the path exists and the user can read it."
	default:
		return "Connection failed — see server log for details."
	}
}

// friendlyFTPError is the FTP/FTPS sibling of friendlyProbeError. The
// underlying error patterns are different — FTPS uses TLS for the
// handshake (not SSH) and FTP returns numeric reply codes (530 for
// auth, 421 for service-not-available, etc.). Mapping them through
// the SFTP-flavoured helper produced misleading "SSH handshake
// failed" messages on FTPS errors. Split out so each helper stays
// straightforward.
func friendlyFTPError(proto protocol.Protocol, err error) string {
	if err == nil {
		return ""
	}
	tag := "FTP"
	if proto == protocol.FTPS {
		tag = "FTPS"
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "no such host"):
		return "Hostname could not be resolved."
	case strings.Contains(low, "connection refused"):
		return tag + " server refused the connection — verify the server is running on this port."
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "deadline exceeded"):
		return "Connection timed out — verify the host is reachable and not firewalled."
	case strings.Contains(low, "network is unreachable"):
		return "Network is unreachable from this host."
	// FTPS / TLS handshake patterns.
	case strings.Contains(low, "tls: handshake failure"),
		strings.Contains(low, "tls handshake error"),
		strings.Contains(low, "remote error: tls"):
		return "TLS handshake failed — the server may not support implicit-TLS on this port, or it may not accept your TLS version. Try `tls_mode: explicit` if the server uses AUTH TLS on port 21."
	case strings.Contains(low, "x509: certificate signed by unknown authority"),
		strings.Contains(low, "certificate is not trusted"),
		strings.Contains(low, "self-signed certificate"):
		return "FTPS certificate is not trusted. Either set `tls_trust_on_first_use: true` to TOFU-pin it, or `tls_insecure_skip_verify: true` for a lab server."
	case strings.Contains(low, "x509: certificate has expired"),
		strings.Contains(low, "certificate has expired or is not yet valid"):
		return "FTPS certificate has expired. The server needs a renewed cert."
	case strings.Contains(low, "x509: certificate is valid for"):
		return "FTPS certificate's SAN/CN doesn't match the host you're connecting to. Override with `tls_server_name` if you're using an alias."
	case strings.Contains(low, "ftp: 530"), strings.Contains(low, "login incorrect"):
		return tag + " login rejected (530) — verify username and password."
	case strings.Contains(low, "ftp: 421"):
		return tag + " server is not available (421) — too many connections, or the server is shutting down."
	case strings.Contains(low, "ftp: 550"):
		return tag + " server denied the request (550) — typically the upload folder doesn't exist or isn't writable."
	case strings.Contains(low, "auth tls"):
		return "Server rejected AUTH TLS — verify the server supports explicit FTPS, or switch to implicit-TLS (`tls_mode: implicit`)."
	case strings.Contains(low, "eof"), strings.Contains(low, "use of closed network connection"):
		return tag + " server closed the connection during handshake — common when implicit-TLS is required but plain FTP was negotiated, or vice versa."
	default:
		return tag + " connection failed — see server log for details."
	}
}

// noStoreAssets wraps a handler with Cache-Control: no-store so the
// WebKit / WKWebView per-app cache doesn't shadow newer embedded
// assets after a binary upgrade. Applied to the static-file handler
// only — JSON API endpoints are already non-cacheable by content type.
func noStoreAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// historicalForLiveID looks up the on-disk meta for a run that is also
// present in memory. Returns nil when the seal hasn't completed yet —
// the caller should fall back to the live-stub view in that case.
func historicalForLiveID(reportsDir, id string) *persist.RunMeta {
	metas, _ := persist.ListMeta(reportsDir)
	for i := range metas {
		if metas[i].ID == id {
			return &metas[i]
		}
	}
	return nil
}

// runMetaToMap renders a RunMeta in the JSON shape the runs-history UI
// expects. Pulled out of /api/runs so live and historical entries pass
// through the same builder — when a finished-but-still-in-memory run
// has its seal complete on disk, the live branch can use this same
// function to render the historical fields.
func runMetaToMap(m persist.RunMeta, _live bool, source string) map[string]any {
	return map[string]any{
		"id":                        m.ID,
		"started_at":                m.StartedAt,
		"stopped_at":                m.StoppedAt,
		"active":                    false,
		"total_files":               m.TotalFiles,
		"total_bytes":               m.TotalBytes,
		"overall_mbps":              m.OverallMBps,
		"failed_files":              m.FailedFiles,
		"succeeded_files":           m.SucceededFiles,
		"upload_users":              m.UploadUsers,
		"download_users":            m.DownloadUsers,
		"parallel_streams":          m.ParallelStreams,
		"download_parallel_streams": m.DownloadParallelStreams,
		"files_per_minute":          m.FilesPerMinute,
		"download_enabled":          m.DownloadEnabled,
		"download_match_mode":       m.DownloadMatchMode,
		"dispatch_skips":            m.DispatchSkips,
		"download_stalled":          m.DownloadStalled,
		"interrupted":               m.Interrupted,
		"latency":                   m.Latency,
		"peak_cpu_percent":          m.PeakCPUPercent,
		"avg_cpu_percent":           m.AvgCPUPercent,
		"peak_fd_in_use":            m.PeakFDInUse,
		"peak_goroutines":           m.PeakGoroutines,
		"peak_heap_mb":              m.PeakHeapMB,
		"peak_window_mbps":          m.PeakWindowMBps,
		"num_cpu":                   m.NumCPU,
		"suggestions":               m.Suggestions,
		"source":                    source,
	}
}

