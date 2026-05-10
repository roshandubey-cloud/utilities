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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/bastion"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostinfo"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/hostkeys"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/latency"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/persist"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/proc"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/protocol"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/quirks"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/report"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/runner"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/alerts"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/source"
	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/vault"
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

	// alertsCfg (v0.15.0) is the global alerts configuration: webhook
	// URLs, SMTP settings, threshold rules. Persisted as alerts.json
	// under reportsDir; loaded on Server creation; mutated via
	// /api/alerts. nil-safe — empty Config disables every channel.
	alertsCfg   alerts.Config
	alertsCfgMu sync.Mutex

	// v0.20.0 — OS-independent encrypted secret store. vaultPath is
	// the on-disk file path (typically <reportsDir>/secrets.vault on
	// the worker, <userConfigDir>/sftp-loadtest/secrets.vault on the
	// desktop app). vaultBinder owns the in-process unlocked Vault;
	// nil means locked. See internal/vault for the cryptography.
	vaultPath   string
	vaultBinder vaultBinder
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
	// v0.15.0 — load alerts config from disk on startup so the channels
	// configured by the operator survive restarts.
	s.loadAlertsConfig()
	return s
}

// pendingTrackIDsSafe reads the watcher's pending count when the run
// is still active, and returns 0 once the watcher has been released
// (post-seal, v0.19.5). The /api/status response paths through this
// helper for any run regardless of state, including sealed runs that
// no longer have a Watcher.
func pendingTrackIDsSafe(run *runner.Run) int {
	if run == nil || run.Watcher == nil {
		return 0
	}
	return run.Watcher.PendingCount()
}

// alertsConfigPath returns the on-disk JSON path for the alerts
// configuration. Lives next to reports so backups capture both.
func (s *Server) alertsConfigPath() string {
	if s.reportsDir == "" {
		return ""
	}
	return filepath.Join(s.reportsDir, "alerts.json")
}

// loadAlertsConfig reads alerts.json on startup. Missing file =
// alerts disabled (zero-value Config). Malformed file = log + zero.
func (s *Server) loadAlertsConfig() {
	p := s.alertsConfigPath()
	if p == "" {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return // no file is fine — alerts disabled
	}
	var cfg alerts.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Printf("alerts: parse %s: %v — alerts disabled", p, err)
		return
	}
	s.alertsCfgMu.Lock()
	s.alertsCfg = cfg
	s.alertsCfgMu.Unlock()
}

// saveAlertsConfig writes the current config back to disk. Atomic
// write via temp + rename so a crash mid-write doesn't corrupt the
// file.
func (s *Server) saveAlertsConfig() error {
	p := s.alertsConfigPath()
	if p == "" {
		return errors.New("reportsDir not configured; alerts cannot persist")
	}
	s.alertsCfgMu.Lock()
	cfg := s.alertsCfg
	s.alertsCfgMu.Unlock()
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// alertsConfig returns a defensive copy of the current alerts config.
func (s *Server) alertsConfig() alerts.Config {
	s.alertsCfgMu.Lock()
	defer s.alertsCfgMu.Unlock()
	return s.alertsCfg
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

// SetVaultPath wires the on-disk path used by the secret-vault
// endpoints (internal/vault). Called from main.go (CLI worker) +
// cmd/desktop/main.go (Wails app) so both surfaces share one code
// path. Empty path disables every /api/vault/* endpoint with a
// 500 — operators that don't want a vault simply don't pass the
// flag.
func (s *Server) SetVaultPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vaultPath = path
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

		// Bastion / SSH ProxyJump (v0.19.x). When BastionHost is set
		// AND protocol resolves to SFTP, the probe opens a bastion
		// session and tunnels the target SSH dial through it — same
		// wiring the runner uses, so the operator validates bastion
		// auth + reachability without starting a real run. Ignored on
		// FTP/FTPS (the runner rejects bastion + non-SFTP combos).
		BastionHost              string `json:"bastion_host,omitempty"`
		BastionPort              int    `json:"bastion_port,omitempty"`
		BastionUser              string `json:"bastion_user,omitempty"`
		BastionPass              string `json:"bastion_pass,omitempty"`
		BastionPrivateKeyPEM     string `json:"bastion_private_key_pem,omitempty"`
		BastionPassphrase        string `json:"bastion_passphrase,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// v0.20.0 — substitute vault refs in credential fields before
	// the underlying SSH/FTP dial. Same boundary the runner uses
	// (resolveStartReqVaultRefs); keeps the dial layer ignorant of
	// the vault.
	if vv := s.vaultBinder.get(); vv != nil {
		req.Password, _, _ = vault.ResolveString(req.Password, vv)
		req.PrivateKey, _, _ = vault.ResolveString(req.PrivateKey, vv)
		req.Passphrase, _, _ = vault.ResolveString(req.Passphrase, vv)
		req.BastionPass, _, _ = vault.ResolveString(req.BastionPass, vv)
		req.BastionPrivateKeyPEM, _, _ = vault.ResolveString(req.BastionPrivateKeyPEM, vv)
		req.BastionPassphrase, _, _ = vault.ResolveString(req.BastionPassphrase, vv)
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

	// Bastion / SSH ProxyJump (v0.19.x). When BastionHost is set, open
	// the jump session up front and tunnel the target SSH dial through
	// it. Mirrors the runner's bastion path so the operator validates
	// jump-host creds + reachability before starting a real run. Bastion
	// is SFTP-only on the runner side; we mirror that here so an FTP/FTPS
	// probe with bastion fields surfaces a clear "not supported" error
	// instead of silently ignoring them.
	if req.BastionHost != "" {
		if proto != protocol.SFTP {
			out["stage"] = "bastion"
			out["error"] = "bastion / SSH ProxyJump is only supported for SFTP"
			writeJSON(w, out)
			return
		}
		bcfg := bastion.Config{
			Host:            req.BastionHost,
			Port:            req.BastionPort,
			User:            req.BastionUser,
			Pass:            req.BastionPass,
			Passphrase:      req.BastionPassphrase,
			HostKeyCallback: sftpx.CurrentCallback(),
		}
		if req.BastionPrivateKeyPEM != "" {
			bcfg.PrivateKey = []byte(req.BastionPrivateKeyPEM)
		}
		bc, berr := bastion.Open(bcfg)
		if berr != nil {
			log.Printf("probe %s: bastion: %v", addr, berr)
			out["stage"] = "bastion"
			out["error"] = "bastion: " + berr.Error()
			writeJSON(w, out)
			return
		}
		// Close the bastion session as soon as the probe returns; the
		// target SFTP client below opens its own forwarded channel and
		// will tear down cleanly when its Close is called.
		defer bc.Close()
		dialOpts.BastionDialer = bc.Dialer()
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

// /api/probe-source — local-only validation of a SourceConfig before a
// real run. Resolves the kind to the same constructors the runner uses
// (NewLocalFiles / NewLocalDir / NewLocalTree), then returns the file
// list with sizes. No network I/O — purely a "did the operator point
// at a real folder / existing files?" check so the desktop UI can
// surface "12 files, 3.4 MB" before they commit to a long run.
//
// Body shape:
//
//	{
//	  "source": { ...SourceConfig... },
//	  "users":  [{"username":"alice","pattern":"invoice-*"}, ...]   // optional
//	}
//
// The legacy v0.14.3 shape — a bare SourceConfig at the body root — is
// still accepted for backwards compat. When `users` is provided AND the
// kind is "local-dir" with a non-flat Layout, the response carries a
// per-user matrix instead of a single file list so an operator running
// "by-user" / "by-pattern" / "by-user-pattern" can verify each
// account's pool resolves before they kick off the run.
func (s *Server) handleProbeSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "read body: " + err.Error()})
		return
	}
	// Try the new envelope first; fall through to bare SourceConfig.
	var env struct {
		Source *config.SourceConfig `json:"source"`
		Users  []struct {
			Username string `json:"username"`
			Pattern  string `json:"pattern"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Source != nil {
		writeJSON(w, probeSourceResolved(*env.Source, env.Users))
		return
	}
	var legacy config.SourceConfig
	if err := json.Unmarshal(body, &legacy); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "bad json: " + err.Error()})
		return
	}
	writeJSON(w, probeSourceResolved(legacy, nil))
}

// probeSourceResolved is the body of /api/probe-source — split out so
// the legacy bare-config and new {source, users} envelopes can share it.
func probeSourceResolved(cfg config.SourceConfig, users []struct {
	Username string `json:"username"`
	Pattern  string `json:"pattern"`
}) map[string]any {
	out := map[string]any{"ok": true, "kind": cfg.Kind, "files": []any{}, "total_bytes": int64(0)}
	switch cfg.Kind {
	case "", "synthetic":
		out["note"] = "synthetic source — random bytes generated per upload"
		return out
	case "local-files":
		lf, err := source.NewLocalFiles(cfg.Files, source.PickMode(cfg.Mode))
		if err != nil {
			return map[string]any{"ok": false, "kind": cfg.Kind, "error": err.Error()}
		}
		out["files"], out["total_bytes"] = statFiles(lf.Files())
		return out
	case "local-dir":
		// Flat layout (default) → eager pool, single list for all users.
		if cfg.Layout == "" || cfg.Layout == "flat" {
			ld, err := source.NewLocalDir(cfg.Dir, source.PickMode(cfg.Mode))
			if err != nil {
				return map[string]any{"ok": false, "kind": cfg.Kind, "error": err.Error()}
			}
			out["layout"] = "flat"
			out["files"], out["total_bytes"] = statFiles(ld.Files())
			return out
		}
		// Per-user / per-pattern layouts need sample users to resolve
		// against. Build the LocalTree and probe each (user, pattern).
		lt, err := source.NewLocalTree(cfg.Dir, source.Layout(cfg.Layout), source.PickMode(cfg.Mode))
		if err != nil {
			return map[string]any{"ok": false, "kind": cfg.Kind, "layout": cfg.Layout, "error": err.Error()}
		}
		out["layout"] = string(lt.LayoutName())
		out["root"] = lt.Root()
		if len(users) == 0 {
			out["note"] = "supply users[{username,pattern}] in the body to resolve per-user pools"
			return out
		}
		matrix := make([]map[string]any, 0, len(users))
		var grandTotal int64
		var grandCount int
		for _, u := range users {
			row := map[string]any{"username": u.Username, "pattern": u.Pattern}
			files, ferr := lt.FilesFor(u.Username, u.Pattern)
			if ferr != nil {
				row["ok"] = false
				row["error"] = ferr.Error()
				matrix = append(matrix, row)
				continue
			}
			fileEntries, total := statFiles(files)
			row["ok"] = true
			row["files"] = fileEntries
			row["total_bytes"] = total
			matrix = append(matrix, row)
			grandTotal += total
			grandCount += len(fileEntries)
		}
		out["users"] = matrix
		out["total_bytes"] = grandTotal
		out["total_files"] = grandCount
		return out
	default:
		return map[string]any{"ok": false, "error": "unknown kind: " + cfg.Kind}
	}
}

// statFiles stats every path in the pool and returns a list of
// {path, size, error?} entries plus the running total. Stat failures
// don't abort the probe — they show up in-line so the operator sees
// exactly which file is missing or unreadable.
func statFiles(paths []string) ([]map[string]any, int64) {
	out := make([]map[string]any, 0, len(paths))
	var total int64
	for _, p := range paths {
		entry := map[string]any{"path": p}
		if fi, err := os.Stat(p); err != nil {
			entry["error"] = err.Error()
		} else {
			entry["size"] = fi.Size()
			total += fi.Size()
		}
		out = append(out, entry)
	}
	return out, total
}

// /api/probe-sink — local-only validation of a SinkConfig. Verifies the
// root directory is writable (creates it if missing — the same lazy
// behaviour the runner uses on first download) and renders the template
// against a sample upload so the operator sees the actual on-disk path
// before committing.
//
// Body: {kind, root, template, overwrite}. Returns the resolved path
// preview + a writable flag.
func (s *Server) handleProbeSink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req config.SinkConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "bad json: " + err.Error()})
		return
	}
	if req.Kind == "" || req.Kind == "discard" {
		writeJSON(w, map[string]any{"ok": true, "kind": "discard", "note": "downloads stream to io.Discard — no on-disk persistence"})
		return
	}
	if req.Kind != "local-disk" {
		writeJSON(w, map[string]any{"ok": false, "error": "unknown kind: " + req.Kind})
		return
	}
	if strings.TrimSpace(req.Root) == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "root is required for local-disk sink"})
		return
	}
	// Lazy create + write probe — same semantics as the runner's first download.
	if err := os.MkdirAll(req.Root, 0o755); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "mkdir " + req.Root + ": " + err.Error()})
		return
	}
	probe := req.Root + "/.sftpl-sink-probe"
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "write probe: " + err.Error()})
		return
	}
	_ = os.Remove(probe)
	writeJSON(w, map[string]any{
		"ok":       true,
		"kind":     "local-disk",
		"root":     req.Root,
		"writable": true,
	})
}

// /api/quirks — returns the list of named server-quirk profiles the UI
// dropdown should render. Static for the lifetime of the binary; ships
// the names from internal/quirks. v0.16.0.
func (s *Server) handleQuirks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"profiles": quirks.Names()})
}

// /api/version — lightweight unauthenticated GET for the masthead.
// Returns {version, started_at} so a fresh page load can render the
// platform version next to the brand without crossing the BasicAuth
// gate that /healthz?detail=1 sits behind. Cache-Control: no-store so
// the WebKit per-app cache on macOS doesn't pin the value across an
// app upgrade.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"version":    s.getVersion(),
		"started_at": processStart.Format(time.RFC3339),
	})
}

// dispatchAlertsWhenDone waits for the run to fully finalize (track-id
// drain + teardown) then evaluates trigger thresholds against the
// final metrics and fires to every configured channel. Runs in a
// goroutine; alert delivery is best-effort and never blocks the run.
func (s *Server) dispatchAlertsWhenDone(run *runner.Run, cfg *config.RunConfig) {
	if run == nil {
		return
	}
	<-run.Done()
	alertCfg := s.alertsConfig()
	if !alertCfg.Anything() {
		return // alerts disabled — fast exit
	}
	// Final metrics.
	m := run.Metrics.Snapshot()
	totalFiles := m.TotalFiles
	failed := run.FailedFiles.Load()
	skips := run.DispatchSkips.Load()
	upSnap := run.UploadLatency.Snapshot()
	p99ms := float64(upSnap.P99) / 1e6 // ns → ms
	errorRate := 0.0
	if totalFiles > 0 {
		errorRate = float64(failed) / float64(totalFiles) * 100
	}
	host := ""
	proto := ""
	if cfg != nil {
		host = cfg.Host
		proto = cfg.Protocol
		if proto == "" {
			proto = "sftp"
		}
	}
	// v0.18.0 — pull stop reason / detail / hash mismatch from the
	// freshly-sealed RunMeta on disk so the alert reflects the same
	// values the CSV trailer and Previous-runs UI carry. Falls back
	// to in-memory snapshot fields when the meta isn't readable.
	var stopReason, stopDetail string
	var hashMismatch int64
	if s.reportsDir != "" {
		if metas, ferr := persist.ListMeta(s.reportsDir); ferr == nil {
			for _, mm := range metas {
				if mm.ID == run.ID {
					stopReason = mm.StopReason
					stopDetail = mm.StopDetail
					hashMismatch = mm.HashMismatch
					break
				}
			}
		}
	}
	ev := alerts.Event{
		Kind:          "run_complete",
		RunID:         run.ID,
		StartedAt:     run.StartedAt,
		EndedAt:       time.Now(),
		Host:          host,
		Protocol:      proto,
		TotalFiles:    totalFiles,
		FailedFiles:   failed,
		TotalBytes:    m.TotalBytes,
		OverallMbps:   m.OverallMBps,
		P99LatencyMS:  p99ms,
		DispatchSkips: skips,
		ErrorRate:     errorRate,
		StopReason:    stopReason,
		StopDetail:    stopDetail,
		HashMismatch:  hashMismatch,
	}
	reasons := alertCfg.ShouldFire(ev)
	if len(reasons) == 0 {
		return // no triggers met — silent success
	}
	ev.Reasons = reasons
	d := alerts.NewDispatcher(alertCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.Fire(ctx, ev)
}

// /api/alerts — GET returns the current alerts.Config; POST replaces
// it. Persisted to alerts.json under reportsDir. Passwords (SMTP)
// are persisted in plaintext — operators that don't want that should
// use a webhook + a CI/CD secret manager and ignore the SMTP fields.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.alertsConfig())
	case http.MethodPost:
		var cfg alerts.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.alertsCfgMu.Lock()
		s.alertsCfg = cfg
		s.alertsCfgMu.Unlock()
		if err := s.saveAlertsConfig(); err != nil {
			http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// /api/alerts/test — fires a synthetic Event through every configured
// channel so the operator can verify their webhook URLs / SMTP creds
// without waiting for a real run to fail.
func (s *Server) handleAlertsTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.alertsConfig()
	if !cfg.Anything() {
		writeJSON(w, map[string]any{"ok": false, "error": "no alert channels configured"})
		return
	}
	d := alerts.NewDispatcher(cfg)
	ev := alerts.Event{
		Kind:          "test",
		RunID:         "test-event",
		StartedAt:     time.Now().Add(-1 * time.Minute),
		EndedAt:       time.Now(),
		Host:          "127.0.0.1",
		Protocol:      "sftp",
		TotalFiles:    100,
		FailedFiles:   2,
		TotalBytes:    104857600,
		OverallMbps:   8.39,
		P99LatencyMS:  87,
		DispatchSkips: 0,
		ErrorRate:     2.0,
		Reasons:       []string{"test alert from /api/alerts/test"},
	}
	d.Fire(r.Context(), ev)
	writeJSON(w, map[string]any{"ok": true, "channels_attempted": channelCount(cfg)})
}

// channelCount reports how many channels would be attempted for an
// alert with the given config. Used by the /api/alerts/test response
// so the operator sees whether all 3 paths were tried (or only
// whatever subset they configured).
func channelCount(cfg alerts.Config) int {
	n := 0
	if cfg.SlackWebhookURL != "" {
		n++
	}
	if cfg.GenericWebhookURL != "" {
		n++
	}
	if cfg.SMTPHost != "" && len(cfg.EmailTo) > 0 {
		n++
	}
	return n
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
		// v0.17.0 — concurrent-run aware. active_run + active_run_id are
		// kept for back-compat (they reflect the most recent active run,
		// matching pre-v0.16 single-run semantics); active_run_count and
		// active_run_ids surface the full picture for monitors that need it.
		actives := s.activeRuns()
		out["uptime_sec"] = int64(time.Since(processStart).Seconds())
		out["active_run"] = len(actives) > 0
		out["active_run_count"] = len(actives)
		ids := make([]string, len(actives))
		for i, r := range actives {
			ids[i] = r.ID
		}
		out["active_run_ids"] = ids
		if len(actives) > 0 {
			out["active_run_id"] = actives[len(actives)-1].ID // most recent
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
	// Cache-Control: no-store. v0.14.19 — also intercepts requests
	// for "/" and "/index.html" to substitute __SFTPL_VERSION__ with
	// the running binary's platformVersion. This eliminates the async
	// /api/version fetch race that had the version pill show up empty
	// when the fetch failed silently in the WebKit AssetServer.
	mux.Handle("/", noStoreAssets(s.indexVersionInjector(http.FileServer(http.FS(sub)))))

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/host", s.handleHost)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/probe-source", s.handleProbeSource)
	mux.HandleFunc("/api/probe-sink", s.handleProbeSink)
	mux.HandleFunc("/api/alerts", s.handleAlerts)
	mux.HandleFunc("/api/alerts/test", s.handleAlertsTest)
	mux.HandleFunc("/api/quirks", s.handleQuirks)
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

	// v0.20.0 — OS-independent encrypted secret vault. See
	// internal/vault and internal/web/vault_handlers.go.
	mux.HandleFunc("/api/vault/status", s.handleVaultStatus)
	mux.HandleFunc("/api/vault/unlock", s.handleVaultUnlock)
	mux.HandleFunc("/api/vault/lock", s.handleVaultLock)
	mux.HandleFunc("/api/vault/set", s.handleVaultSet)
	mux.HandleFunc("/api/vault/get", s.handleVaultGet)
	mux.HandleFunc("/api/vault/delete", s.handleVaultDelete)
	mux.HandleFunc("/api/vault/list", s.handleVaultList)
	mux.HandleFunc("/api/vault/change-passphrase", s.handleVaultChangePassphrase)
	mux.HandleFunc("/api/vault/migrate-scan", s.handleVaultMigrateScan)
	mux.HandleFunc("/api/vault/migrate-apply", s.handleVaultMigrateApply)
	// v0.20.4 — Run Doctor (AI run analyzer). Endpoints live in
	// rundoctor_handlers.go; AI key + provider live in the encrypted
	// vault under refs "ai/api_key" / "ai/provider".
	mux.HandleFunc("/api/run-doctor/config", s.handleRunDoctorConfig)
	mux.HandleFunc("/api/run-doctor/peers", s.handleRunDoctorPeers)
	mux.HandleFunc("/api/run-doctor/analyze", s.handleRunDoctorAnalyze)
	// v0.20.6 — diagnosis history (per-run thread of saved
	// diagnoses + follow-up Q&As) and supported-model metadata.
	mux.HandleFunc("/api/run-doctor/history", s.handleRunDoctorHistory)
	mux.HandleFunc("/api/run-doctor/models", s.handleRunDoctorModels)
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
//
// v0.16.0 — when multiple runs are active concurrently, this returns the
// most recently started one (matches the legacy single-run behaviour for
// callers like /api/health and the schedule gate). Callers that need the
// full active set use activeRuns().
func (s *Server) activeRun() *runner.Run {
	run := s.latest()
	if run == nil {
		return nil
	}
	if run.IsActive() {
		return run
	}
	// Latest is finished — fall back to scanning for any still-active run.
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		r := s.runs[s.order[i]]
		if r != nil && r.IsActive() {
			return r
		}
	}
	return nil
}

// activeRuns returns every currently-active run, ordered by start time
// (oldest first). Empty slice when nothing is running. Used by /api/stop
// to disambiguate when multiple runs are in flight.
func (s *Server) activeRuns() []*runner.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*runner.Run, 0, len(s.order))
	for _, id := range s.order {
		r := s.runs[id]
		if r != nil && r.IsActive() {
			out = append(out, r)
		}
	}
	return out
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

	// TLSPolicy (v0.15.0) clamps the minimum TLS version. "" / "default"
	// = Go default (TLS 1.2 minimum); "modern" / "tls13" = TLS 1.3
	// only; "legacy" = TLS 1.0 minimum. Wired into RunConfig.TLSPolicy
	// at /api/start time and from there into protocol.DialOpts.
	TLSPolicy string `json:"tls_policy,omitempty"`

	// QuirkProfile (v0.16.0) selects a named server-quirk profile.
	// "" / "default" = no overrides. See internal/quirks for the list.
	QuirkProfile string `json:"quirk_profile,omitempty"`

	// Bastion / SSH ProxyJump (v0.16.0). Optional. When BastionHost is
	// set, the SFTP run dials through the jump host. Auth uses
	// BastionPass, or BastionPrivateKeyPEM (with optional
	// BastionPassphrase). SFTP-only — the runner rejects FTP/FTPS
	// runs with a bastion configured.
	BastionHost          string `json:"bastion_host,omitempty"`
	BastionPort          int    `json:"bastion_port,omitempty"`
	BastionUser          string `json:"bastion_user,omitempty"`
	BastionPass          string `json:"bastion_pass,omitempty"`
	BastionPrivateKeyPEM string `json:"bastion_private_key_pem,omitempty"`
	BastionPassphrase    string `json:"bastion_passphrase,omitempty"`

	// v0.18.0 — speed-floor auto-stop. SpeedFloorPercent of 0 disables
	// the check; >0 means "stop when current Mbps drops below
	// (peak * percent/100)". SpeedFloorWarmupSec defers the first
	// evaluation; default 60s when zero.
	SpeedFloorPercent   int `json:"speed_floor_percent,omitempty"`
	SpeedFloorWarmupSec int `json:"speed_floor_warmup_sec,omitempty"`
	SpeedFloorBreachSec int `json:"speed_floor_breach_sec,omitempty"`

	// v0.18.0 — end-to-end SHA-256 verification of every uploaded
	// file against its corresponding download. False (default)
	// preserves the v0.17.x behaviour: no hashing, no extra columns.
	VerifyHashes bool `json:"verify_hashes,omitempty"`

	NormalEnabled     bool   `json:"normal_enabled"`
	FilesPerMinute    int    `json:"files_per_minute"`
	NormalMinMB       int    `json:"normal_min_mb"`
	NormalMaxMB       int    `json:"normal_max_mb"`
	NormalContentType string `json:"normal_content_type"`
	NormalUsersCSV    string `json:"normal_users_csv"`

	// v0.15.0 — step-load ramp. Optional. When start_fpm > 0, the runner
	// ramps fpm over time instead of using FilesPerMinute as a fixed
	// rate. Wired into config.NormalLoad.Ramp at /api/start time.
	NormalRamp *config.RampConfig `json:"normal_ramp,omitempty"`

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

	// TargetUsername / TargetPassword are single-user probe credentials
	// for the Test-connection button. They DON'T drive the run (the
	// run pulls users from the per-load CSVs); they round-trip through
	// Export / Import so an operator who saved a config to disk can
	// re-test the connection without retyping. Decoder-tolerant only —
	// the runner ignores them.
	TargetUsername string `json:"target_username,omitempty"`
	TargetPassword string `json:"target_password,omitempty"`

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
		TLSPolicy:              req.TLSPolicy,
		QuirkProfile:           req.QuirkProfile,
		BastionHost:            req.BastionHost,
		BastionPort:            req.BastionPort,
		BastionUser:            req.BastionUser,
		BastionPass:            req.BastionPass,
		BastionPrivateKeyPEM:   req.BastionPrivateKeyPEM,
		BastionPassphrase:      req.BastionPassphrase,
		SpeedFloorPercent:      req.SpeedFloorPercent,
		SpeedFloorWarmupSec:    req.SpeedFloorWarmupSec,
		SpeedFloorBreachSec:    req.SpeedFloorBreachSec,
		VerifyHashes:           req.VerifyHashes,
	}
	if req.NormalEnabled {
		cfg.Normal = &config.NormalLoad{
			FilesPerMinute: req.FilesPerMinute,
			MinSizeMB:      req.NormalMinMB,
			MaxSizeMB:      req.NormalMaxMB,
			ContentType:    req.NormalContentType,
			Source:         req.NormalSource, // nil-safe; keeps synthetic default
			Ramp:           req.NormalRamp,   // nil-safe; uses fixed FilesPerMinute
		}
		if err := cfg.Normal.Ramp.Validate(); err != nil {
			return nil, fmt.Errorf("normal ramp: %w", err)
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
		users, err := config.ParseDownloadUsersCSV(strings.NewReader(req.DownloadUsersCSV))
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
	// v0.16.0 — concurrent runs allowed. The hard "one run at a time"
	// gate is gone; each run gets its own ID, pools, watcher, metrics,
	// and persist files. Operators can still stop runs individually via
	// /api/stop?run=<id>. The schedule sweep keeps its own gate (see
	// schedule.go) so cron firings never spawn surprise overlaps.
	// v0.20.0 — resolve any vault refs in the request BEFORE
	// buildRunConfig sees them. The runner doesn't know about the
	// vault; refs are a UI / persistence concern. By resolving at
	// the boundary we keep the runner's surface unchanged.
	if v := s.vaultBinder.get(); v != nil {
		req = resolveStartReqVaultRefs(req, v)
	}
	cfg, err := buildRunConfig(req)
	if err != nil {
		return nil, err
	}
	// v0.17.1 — concurrent download sink safeguard. When a sibling
	// active run is also writing to local-disk under the same Root and
	// our template doesn't include {run_id}, files would clobber across
	// runs (overwrite=true) or every download would fail with EEXIST
	// (overwrite=false). Auto-prepend "{run_id}/" so paths stay
	// disjoint. The default template already includes {run_id} since
	// v0.17.1; this catches operator-supplied templates that don't.
	maybePrependRunIDToSink(cfg, s.activeRunsSnapshot())
	run, err := runner.StartWithPersistAndTLS(context.Background(), cfg, s.reportsDir, s.getTLSStore())
	if err != nil {
		return nil, err
	}
	run.StartedBy = startedBy
	// v0.15.0 — alert dispatch on run completion. Goroutine waits for
	// run.Done() (full teardown after track-id drain), evaluates
	// triggers against the final metrics, and fires to every
	// configured channel. Non-blocking; errors get logged.
	go s.dispatchAlertsWhenDone(run, cfg)
	s.mu.Lock()
	s.runs[run.ID] = run
	s.order = append(s.order, run.ID)
	// v0.17.0 — eviction now walks the slice for the oldest *finished*
	// run instead of bailing on the first active. The pre-v0.17 loop
	// stopped at any active head, so when concurrent active runs ≥
	// maxRetainedRuns the slice grew unbounded. The new loop preserves
	// every active run (they keep their slot) and evicts the oldest
	// finished one until the cap is satisfied — or the slice is all
	// active, in which case we accept temporary over-cap until any
	// run completes (then the next start trims).
	for len(s.order) > maxRetainedRuns {
		evicted := false
		for i, id := range s.order {
			r := s.runs[id]
			if r == nil || !r.IsActive() {
				delete(s.runs, id)
				s.order = append(s.order[:i], s.order[i+1:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			break // every retained run is active; cap relaxes once any finishes
		}
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
			// Bastion / SSH ProxyJump (v0.19.x). When the run is configured
			// with a bastion, the preflight must traverse it too — a direct
			// dial to a target only reachable via the jump host would TCP-fail
			// and the consent prompt would never fire. Pass the same fields
			// the run will use; preflightHostKey opens the bastion, dials,
			// closes both ends so this is an end-to-end pin of the wiring.
			var preBastion *bastionPreflight
			if req.BastionHost != "" && preflightProto == protocol.SFTP {
				preBastion = &bastionPreflight{
					Host: req.BastionHost, Port: req.BastionPort,
					User: req.BastionUser, Pass: req.BastionPass,
					PrivateKeyPEM: req.BastionPrivateKeyPEM, Passphrase: req.BastionPassphrase,
				}
			}
			if pre := s.preflightHostKey(req.Host, req.Port, creds.user, creds.pass, preAuth, preBastion); pre != nil {
				writeJSON(w, pre)
				return
			}
		}
	}

	// v0.17.0 — soft self-DoS guardrail. When concurrent runs are
	// allowed (post-v0.16) operators can accidentally point two runs at
	// the same host:port and self-throttle. We still start the run (no
	// hard block — sometimes concurrent loads against the same host are
	// the test), but surface a warning in the response body so the UI
	// can show a toast. force=true in the URL skips the guardrail
	// silently for automation.
	var warning string
	if r.URL.Query().Get("force") != "true" {
		if conflict := s.findActiveAtTarget(req.Host, req.Port); conflict != "" {
			warning = "another run (" + conflict + ") is already active against " +
				req.Host + ":" + strconv.Itoa(req.Port) +
				" — concurrent runs against the same host share bandwidth and connections"
			// v0.17.1 — also flag download-sink collisions when the
			// operator's template doesn't include {run_id}. The
			// safeguard in startRun auto-prepends in that case, but
			// the operator should know we did it so on-disk paths
			// match expectations.
			if req.DownloadEnabled && req.DownloadSink != nil &&
				req.DownloadSink.Kind == "local-disk" &&
				req.DownloadSink.Template != "" &&
				!strings.Contains(req.DownloadSink.Template, "{run_id}") {
				warning += "; the download sink template lacks {run_id} so paths will be auto-prefixed to keep runs disjoint"
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
	out := map[string]any{"run_id": run.ID}
	if warning != "" {
		out["warning"] = warning
	}
	writeJSON(w, out)
}

// activeRunsSnapshot returns a snapshot of every active run's *RunConfig
// (pointer, not a copy — read-only). Used by startRun to detect sink
// path collisions before launching a new run. Holds s.mu only for the
// walk, not for the caller's subsequent inspection.
func (s *Server) activeRunsSnapshot() []*config.RunConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*config.RunConfig, 0, len(s.order))
	for _, id := range s.order {
		r := s.runs[id]
		if r != nil && r.IsActive() && r.Cfg != nil {
			out = append(out, r.Cfg)
		}
	}
	return out
}

// maybePrependRunIDToSink mutates cfg.Download.Sink.Template to start
// with "{run_id}/" when a sibling active run shares the same sink Root
// and our template doesn't already use {run_id}. No-op when:
//   - download is disabled,
//   - sink kind is not local-disk,
//   - the operator-supplied template already includes {run_id},
//   - no active sibling writes to the same Root.
//
// Idempotent: prefixing twice would render once (the second {run_id}
// would just expand to the same string), but we still avoid duplicates
// for clarity.
func maybePrependRunIDToSink(cfg *config.RunConfig, active []*config.RunConfig) {
	if cfg == nil || cfg.Download == nil || cfg.Download.Sink == nil {
		return
	}
	sk := cfg.Download.Sink
	if sk.Kind != "local-disk" || sk.Root == "" {
		return
	}
	if sk.Template == "" {
		return // empty template uses default which already has {run_id}
	}
	if strings.Contains(sk.Template, "{run_id}") {
		return
	}
	collides := false
	for _, other := range active {
		if other == nil || other.Download == nil || other.Download.Sink == nil {
			continue
		}
		os := other.Download.Sink
		if os.Kind == "local-disk" && os.Root == sk.Root {
			collides = true
			break
		}
	}
	if !collides {
		return
	}
	sk.Template = "{run_id}/" + sk.Template
}

// findActiveAtTarget returns the ID of the first active run whose
// configured Host:Port matches the supplied target, or "" when none
// match. Used by the start handler's self-DoS guardrail. Read-only;
// holds s.mu just long enough to walk the order slice.
func (s *Server) findActiveAtTarget(host string, port int) string {
	if host == "" || port <= 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		r := s.runs[id]
		if r == nil || !r.IsActive() || r.Cfg == nil {
			continue
		}
		if r.Cfg.Host == host && r.Cfg.Port == port {
			return id
		}
	}
	return ""
}

// bastionPreflight carries the optional bastion fields through to
// preflightHostKey when /api/start runs with a configured jump host.
// nil = direct dial (legacy behaviour).
type bastionPreflight struct {
	Host          string
	Port          int
	User          string
	Pass          string
	PrivateKeyPEM string
	Passphrase    string
}

// preflightHostKey runs a single capture-callback dial. Returns a non-nil
// JSON payload only when the UI needs to show a consent prompt — unknown
// host (requires_consent) or changed host key (requires_renewal). All other
// errors (TCP refused, auth failure, etc.) are tolerated here so the actual
// run can start and propagate them as ordinary per-file failures.
//
// Mode selection mirrors handleProbe: store-mode when SetHostKeyStore was
// called, file-mode otherwise. When bp is non-nil, the dial traverses the
// bastion exactly like the run will — closes the bastion before returning
// so this preflight has zero ongoing footprint.
func (s *Server) preflightHostKey(host string, port int, user, pass string, auth []ssh.AuthMethod, bp *bastionPreflight) map[string]any {
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
	// Bastion / SSH ProxyJump (v0.19.x). When the run is configured to
	// use a jump host, the preflight must traverse it — otherwise a
	// target only reachable through the bastion would TCP-fail here
	// and the host-key consent prompt would never fire on /api/start.
	// Open it, hand the dialer to sftpx, close it before returning so
	// the preflight leaves no SSH session behind.
	if bp != nil && bp.Host != "" {
		bcfg := bastion.Config{
			Host:            bp.Host,
			Port:            bp.Port,
			User:            bp.User,
			Pass:            bp.Pass,
			Passphrase:      bp.Passphrase,
			HostKeyCallback: sftpx.CurrentCallback(),
		}
		if bp.PrivateKeyPEM != "" {
			bcfg.PrivateKey = []byte(bp.PrivateKeyPEM)
		}
		bc, berr := bastion.Open(bcfg)
		if berr != nil {
			log.Printf("start preflight bastion %s: %v", bp.Host, berr)
			// Treat bastion failure as tolerable here — the run will
			// fail loudly with the same error and the operator sees it
			// in /api/start's response.
			return nil
		}
		defer bc.Close()
		dialOpts.BastionDialer = bc.Dialer()
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
	// v0.16.0 — with concurrent runs, /api/stop must disambiguate. If
	// the operator passed ?run=<id>, target that one. Otherwise stop the
	// single active run if exactly one exists; refuse with 409 + the
	// list when multiple are active so the UI can prompt for which.
	if id := r.URL.Query().Get("run"); id != "" {
		s.mu.Lock()
		run := s.runs[id]
		s.mu.Unlock()
		if run == nil {
			http.Error(w, "unknown run id", http.StatusNotFound)
			return
		}
		if !run.IsActive() {
			http.Error(w, "run already stopped", http.StatusConflict)
			return
		}
		run.Stop()
		writeJSON(w, map[string]any{"stopped": true, "run_id": run.ID})
		return
	}
	active := s.activeRuns()
	switch len(active) {
	case 0:
		http.Error(w, "no active run", http.StatusNotFound)
	case 1:
		active[0].Stop()
		writeJSON(w, map[string]any{"stopped": true, "run_id": active[0].ID})
	default:
		ids := make([]string, len(active))
		for i, r := range active {
			ids[i] = r.ID
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":      "multiple runs active — pass ?run=<id> to disambiguate",
			"active_ids": ids,
		})
	}
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
		"pending_trackids":    pendingTrackIDsSafe(run),
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
			"download":   latencyStageJSON(run.DownloadLatency.Snapshot()),
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
			// v0.19.16 — surface the same observability fields the
			// post-seal historical entry carries, so a live run's card
			// shows download progress + error breakdown immediately
			// instead of going dark until seal.
			"download_completed": run.DownloadCompleted.Load(),
			"download_orphans":   run.DownloadOrphans.Load(),
			"download_dropped":   run.DownloadDropped.Load(),
			"errors_by_code":     run.ErrorsByCode(),
			"source":             "memory",
		}
		if run.Cfg != nil {
			// v0.20.4 — target host so the live entry carries the same
			// destination tag as the post-seal historical entry.
			entry["target_host"] = run.Cfg.Host
			entry["target_port"] = run.Cfg.Port
			entry["target_protocol"] = run.Cfg.Protocol
			entry["upload_users"] = len(run.Cfg.NormalUsers)
			entry["parallel_streams"] = run.Cfg.ParallelStreams
			entry["large_enabled"] = run.Cfg.LargeFile != nil
			if run.Cfg.Normal != nil {
				entry["normal_enabled"] = true
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
			if run.Cfg.VerifyHashes {
				verified, mismatch := run.Report.HashCounts()
				entry["hash_verified"] = verified
				entry["hash_mismatch"] = mismatch
			}
		}
		// Latency: snapshot the live histograms so percentile points
		// appear immediately on the card, not just after the seal.
		entry["latency"] = map[string]any{
			"upload":     latencyStageJSON(run.UploadLatency.Snapshot()),
			"upload_cor": latencyStageJSON(run.UploadLatencyCOR.Snapshot()),
			"dial":       latencyStageJSON(run.DialLatency.Snapshot()),
			"download":   latencyStageJSON(run.DownloadLatency.Snapshot()),
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

// indexVersionInjector intercepts requests for "/" and "/index.html"
// and substitutes the literal `__SFTPL_VERSION__` token with the
// running binary's platformVersion. Other asset paths pass through
// unchanged. v0.14.19 — was an async /api/version fetch that failed
// silently in some Wails AssetServer paths, leaving the masthead +
// status-bar version cells blank. Server-rendering removes the
// failure mode entirely: the byte the browser receives ALREADY has
// the version stamped in, no JS race, no fetch.
func (s *Server) indexVersionInjector(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			next.ServeHTTP(w, r)
			return
		}
		// Pull the embedded index.html ourselves so we can substitute
		// before sending. Going through the next handler would commit
		// headers + bytes before we can edit.
		sub, _ := fs.Sub(staticFS, "static")
		raw, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ver := s.getVersion()
		if ver == "" {
			ver = "dev"
		}
		body := strings.ReplaceAll(string(raw), "__SFTPL_VERSION__", ver)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body))
	})
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
	// v0.19.16 — `/api/runs` was a hand-curated whitelist that lagged the
	// RunMeta schema: every new field added in v0.19.12-15
	// (download_completed/orphans/dropped, errors_by_code, hash_verified
	// /mismatch, stop_reason/detail, normal_enabled/large_enabled,
	// concurrent_runs_at_peak, disabled[]) was already in the sealed JSON
	// but came back null on the runs-history cards because it wasn't
	// listed here. Now mirrors the full schema.
	return map[string]any{
		"id":                        m.ID,
		"started_at":                m.StartedAt,
		"stopped_at":                m.StoppedAt,
		"active":                    false,
		// v0.20.4 — target host info so Run Doctor + the history UI
		// can show "what server did this hit?" + filter same-host peers.
		"target_host":               m.TargetHost,
		"target_port":               m.TargetPort,
		"target_protocol":           m.TargetProtocol,
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
		"normal_enabled":            m.NormalEnabled,
		"large_enabled":             m.LargeEnabled,
		"download_enabled":          m.DownloadEnabled,
		"download_match_mode":       m.DownloadMatchMode,
		"dispatch_skips":            m.DispatchSkips,
		"download_stalled":          m.DownloadStalled,
		"download_completed":        m.DownloadCompleted,
		"download_orphans":          m.DownloadOrphans,
		"download_dropped":          m.DownloadDropped,
		"errors_by_code":            m.ErrorsByCode,
		"hash_verified":             m.HashVerified,
		"hash_mismatch":             m.HashMismatch,
		"stop_reason":               m.StopReason,
		"stop_detail":               m.StopDetail,
		"concurrent_runs_at_peak":   m.ConcurrentRunsAtPeak,
		"disabled":                  m.Disabled,
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

