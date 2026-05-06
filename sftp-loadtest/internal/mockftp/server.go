// Package mockftp is the in-process mock FTP / FTPS server used by the
// e2e and unit suites. It speaks just enough of RFC 959 + RFC 4217 to
// satisfy github.com/jlaffaye/ftp:
//
//   USER, PASS, TYPE, FEAT, PWD, CWD, SYST, NOOP, OPTS UTF8 ON
//   AUTH TLS, PBSZ 0, PROT P (FTPS explicit upgrade)
//   PASV / EPSV (passive data connections)
//   STOR / LIST / RETR / DELE / RNFR-RNTO / SIZE / MDTM
//   QUIT
//
// The filesystem layer mirrors mocksftp: per-user inbox/outbox/sent with a
// trackid promotion delay (file landed in inbox is renamed to "<n>#<id>"
// after Options.Delay, and a copy is dropped in the destination user's
// outbox). The promote-on-list lazy model keeps this single-threaded and
// easy to reason about.
//
// TLS modes:
//   • Plain: bind a TCP listener; never enable TLS.
//   • Implicit: bind a TLS listener; greet over TLS from byte 0.
//   • Explicit: bind plain; on AUTH TLS, upgrade the control conn in place
//     using crypto/tls.Server.
package mockftp

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configures Start.
type Options struct {
	// Addr is the control-channel listen address. "127.0.0.1:0" picks a
	// free port. Required.
	Addr string
	// Delay is the simulated server-side processing delay before a freshly-
	// uploaded inbox file is renamed to "<name>#<trackid>". Defaults to 2s.
	Delay time.Duration
	// Pairs maps an upload-user to its download-user (the destination outbox).
	// Unpaired users self-loop.
	Pairs map[string]string
	// FailUsers blacklists usernames whose uploads always fail (test harness).
	FailUsers map[string]bool
	// TLS enables FTPS. When non-nil:
	//   • Implicit: the server greets over TLS from byte 0 on Addr.
	//   • Explicit: the server stays plain on Addr but accepts AUTH TLS.
	// Both modes work simultaneously when EnableImplicit + EnableExplicit are set;
	// most tests pick one. The cert + key are auto-generated unless TLSCertPEM is set.
	TLS *TLSOptions
	// Logger overrides the default logger for diagnostic output.
	Logger *log.Logger

	// PersistContent makes the mock retain uploaded bytes in memory and
	// stream them back verbatim on RETR. Off by default — the historical
	// behaviour of synthesising zero-filled downloads keeps high-throughput
	// load runs cheap. Turn ON to validate hash-verify byte fidelity over
	// FTPS. v0.19.9.
	PersistContent bool

	// EvictAfterRead drops a file's outbox + source-side inbox + sent
	// entries from memory the moment its outbox copy is opened for RETR.
	// Pairs with PersistContent for hours-long FTPS hash-verify runs that
	// would otherwise hit the Docker memory ceiling at ~30 min of 2 MB
	// uploads at 60 fpm. v0.19.9. Mirrors the same flag on mocksftp.
	EvictAfterRead bool
}

// TLSOptions parameterise the FTPS test paths.
type TLSOptions struct {
	// EnableImplicit binds an additional TLS listener at ImplicitAddr that
	// expects TLS from byte 0. Useful when a test wants both modes from
	// the same Server instance.
	EnableImplicit bool
	ImplicitAddr   string
	// EnableExplicit honours AUTH TLS on the plain control channel.
	EnableExplicit bool
	// CertPEM / KeyPEM, when both non-empty, override the auto-generated
	// self-signed cert. Useful for fingerprint-pinning tests.
	CertPEM, KeyPEM []byte
}

// Server is a running mock FTP listener.
type Server struct {
	plain       net.Listener
	implicit    net.Listener
	tlsConfig   *tls.Config
	logger      *log.Logger
	wg          sync.WaitGroup
	stopped     chan struct{}
	closeOnce   sync.Once
	fs          *mockFS

	// Cert + fingerprint — exposed for tests so they can pin the
	// fingerprint without reaching into x509.
	certDER     []byte
	fingerprint string
}

// Addr returns the plain (or implicit-only) listener address.
func (s *Server) Addr() net.Addr {
	if s.plain != nil {
		return s.plain.Addr()
	}
	return s.implicit.Addr()
}

// ImplicitAddr returns the implicit-TLS listener address (nil when not
// enabled). Tests dial here for FTPS implicit-mode probes.
func (s *Server) ImplicitAddr() net.Addr {
	if s.implicit == nil {
		return nil
	}
	return s.implicit.Addr()
}

// CertPEM returns the leaf cert in PEM form so tests can pin it.
func (s *Server) CertPEM() []byte {
	if s.certDER == nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.certDER})
}

// Fingerprint returns "SHA256:<hex>" of the leaf cert's DER bytes — same
// shape protocol.Fingerprint produces, so tests can compare directly.
func (s *Server) Fingerprint() string { return s.fingerprint }

// Stop closes listeners and waits for goroutines.
func (s *Server) Stop() error {
	s.closeOnce.Do(func() { close(s.stopped) })
	if s.plain != nil {
		s.plain.Close()
	}
	if s.implicit != nil {
		s.implicit.Close()
	}
	s.wg.Wait()
	return nil
}

// Start launches the mock. The accept loop runs in a goroutine.
func Start(opts Options) (*Server, error) {
	if opts.Addr == "" && (opts.TLS == nil || !opts.TLS.EnableImplicit) {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.Delay == 0 {
		opts.Delay = 2 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	s := &Server{
		logger:  logger,
		stopped: make(chan struct{}),
		fs:      newMockFS(opts.Delay, opts.Pairs, opts.FailUsers),
	}
	s.fs.persist = opts.PersistContent
	s.fs.evictAfterRead = opts.EvictAfterRead

	// Materialise a TLS config when any FTPS mode is on.
	if opts.TLS != nil && (opts.TLS.EnableExplicit || opts.TLS.EnableImplicit) {
		certDER, certPEM, keyPEM, err := buildSelfSignedCert(opts.TLS.CertPEM, opts.TLS.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("tls cert: %w", err)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("tls keypair: %w", err)
		}
		s.tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		s.certDER = certDER
		sum := sha256.Sum256(certDER)
		s.fingerprint = "SHA256:" + hex.EncodeToString(sum[:])
	}

	if opts.Addr != "" {
		l, err := net.Listen("tcp", opts.Addr)
		if err != nil {
			return nil, fmt.Errorf("listen plain: %w", err)
		}
		s.plain = l
		s.wg.Add(1)
		go s.acceptLoop(l, false, opts.TLS != nil && opts.TLS.EnableExplicit)
	}
	if opts.TLS != nil && opts.TLS.EnableImplicit {
		addr := opts.TLS.ImplicitAddr
		if addr == "" {
			addr = "127.0.0.1:0"
		}
		l, err := tls.Listen("tcp", addr, s.tlsConfig)
		if err != nil {
			if s.plain != nil {
				s.plain.Close()
			}
			return nil, fmt.Errorf("listen tls: %w", err)
		}
		s.implicit = l
		s.wg.Add(1)
		go s.acceptLoop(l, true, false)
	}
	logger.Printf("mockftp listening plain=%v implicit=%v delay=%s pairs=%v",
		listenerAddr(s.plain), listenerAddr(s.implicit), opts.Delay, s.fs.routes)
	return s, nil
}

func listenerAddr(l net.Listener) string {
	if l == nil {
		return "<off>"
	}
	return l.Addr().String()
}

func (s *Server) acceptLoop(l net.Listener, alreadyTLS, allowExplicit bool) {
	defer s.wg.Done()
	for {
		c, err := l.Accept()
		if err != nil {
			select {
			case <-s.stopped:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Printf("mockftp accept: %v", err)
			continue
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handle(c, alreadyTLS, allowExplicit)
		}(c)
	}
}

// handle drives one control connection through the FTP command FSM.
func (s *Server) handle(rawConn net.Conn, alreadyTLS, allowExplicit bool) {
	defer rawConn.Close()
	var conn net.Conn = rawConn
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	write := func(line string) {
		bw.WriteString(line)
		bw.WriteString("\r\n")
		bw.Flush()
	}
	write("220 mockftp ready")

	sess := &session{srv: s, dataPort: 0, protectData: alreadyTLS}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg := splitCmd(line)
		switch cmd {
		case "USER":
			sess.user = arg
			write("331 password required")
		case "PASS":
			// Mock accepts ANY password — same posture as mocksftp.
			if sess.user == "" {
				write("530 USER first")
				continue
			}
			sess.authed = true
			write("230 logged in")
		case "AUTH":
			if !allowExplicit || s.tlsConfig == nil {
				write("502 AUTH not supported")
				continue
			}
			if !strings.EqualFold(arg, "TLS") && !strings.EqualFold(arg, "TLS-C") {
				write("504 only TLS")
				continue
			}
			write("234 ready for TLS")
			tlsConn := tls.Server(rawConn, s.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				s.logger.Printf("mockftp AUTH TLS handshake: %v", err)
				return
			}
			conn = tlsConn
			br = bufio.NewReader(conn)
			bw = bufio.NewWriter(conn)
			write = func(line string) {
				bw.WriteString(line)
				bw.WriteString("\r\n")
				bw.Flush()
			}
			sess.protectControl = true
		case "PBSZ":
			write("200 PBSZ=0")
		case "PROT":
			if strings.EqualFold(arg, "P") {
				sess.protectData = true
				write("200 data protection set to PRIVATE")
			} else {
				sess.protectData = false
				write("200 data protection cleared")
			}
		case "FEAT":
			write("211-features")
			write(" UTF8")
			write(" PASV")
			write(" EPSV")
			write(" SIZE")
			write(" AUTH TLS")
			write(" PBSZ")
			write(" PROT")
			write("211 end")
		case "OPTS":
			write("200 ok")
		case "SYST":
			write("215 UNIX Type: L8")
		case "TYPE":
			write("200 type set")
		case "PWD":
			write(`257 "/" current directory`)
		case "CWD":
			write("250 cwd ok")
		case "NOOP":
			write("200 noop")
		case "QUIT":
			write("221 bye")
			return
		case "PASV":
			ip, port, err := sess.openPassive(s)
			if err != nil {
				write("425 cannot open data connection: " + err.Error())
				continue
			}
			parts := strings.Split(ip, ".")
			if len(parts) != 4 {
				parts = []string{"127", "0", "0", "1"}
			}
			p1 := port / 256
			p2 := port % 256
			write(fmt.Sprintf("227 entering passive mode (%s,%s,%s,%s,%d,%d)", parts[0], parts[1], parts[2], parts[3], p1, p2))
		case "EPSV":
			_, port, err := sess.openPassive(s)
			if err != nil {
				write("425 cannot open data connection: " + err.Error())
				continue
			}
			write(fmt.Sprintf("229 entering extended passive mode (|||%d|)", port))
		case "STOR":
			if !sess.authed {
				write("530 not logged in")
				continue
			}
			if !sess.dataReady() {
				write("425 PASV first")
				continue
			}
			if s.fs.failUsers[sess.user] {
				sess.closeData()
				write("550 test harness rejection")
				continue
			}
			write("150 opening data connection")
			n, derr := sess.acceptStor(s.fs, arg)
			if derr != nil {
				write("550 stor failed: " + derr.Error())
				continue
			}
			_ = n
			write("226 transfer complete")
		case "RETR":
			if !sess.authed {
				write("530 not logged in")
				continue
			}
			if !sess.dataReady() {
				write("425 PASV first")
				continue
			}
			body, ok := s.fs.read(sess.user, arg)
			if !ok {
				sess.closeData()
				write("550 not found")
				continue
			}
			write("150 opening data connection")
			if err := sess.streamRetr(body); err != nil {
				write("426 transfer error")
				continue
			}
			write("226 transfer complete")
		case "LIST", "NLST":
			if !sess.authed {
				write("530 not logged in")
				continue
			}
			if !sess.dataReady() {
				write("425 PASV first")
				continue
			}
			s.fs.promoteAll()
			entries := s.fs.listForUser(sess.user, arg)
			write("150 opening data connection")
			if err := sess.streamList(entries); err != nil {
				write("426 list transfer error")
				continue
			}
			write("226 list complete")
		case "DELE":
			if s.fs.delete(sess.user, arg) {
				write("250 deleted")
			} else {
				write("550 not found")
			}
		case "RNFR":
			sess.rnfr = arg
			write("350 ready for RNTO")
		case "RNTO":
			if sess.rnfr == "" {
				write("503 RNFR first")
				continue
			}
			if s.fs.rename(sess.user, sess.rnfr, arg) {
				write("250 renamed")
			} else {
				write("550 rename failed")
			}
			sess.rnfr = ""
		case "SIZE":
			if size, ok := s.fs.size(sess.user, arg); ok {
				write(fmt.Sprintf("213 %d", size))
			} else {
				write("550 not found")
			}
		default:
			write("502 not implemented: " + cmd)
		}
	}
}

func splitCmd(line string) (cmd, arg string) {
	idx := strings.IndexByte(line, ' ')
	if idx < 0 {
		return strings.ToUpper(strings.TrimSpace(line)), ""
	}
	return strings.ToUpper(strings.TrimSpace(line[:idx])), strings.TrimSpace(line[idx+1:])
}

// session is the per-connection state.
type session struct {
	srv            *Server
	user           string
	authed         bool
	rnfr           string
	protectControl bool
	protectData    bool

	dataMu       sync.Mutex
	dataListener net.Listener
	dataPort     int
}

func (s *session) dataReady() bool {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	return s.dataListener != nil
}

func (s *session) closeData() {
	s.dataMu.Lock()
	if s.dataListener != nil {
		s.dataListener.Close()
		s.dataListener = nil
	}
	s.dataMu.Unlock()
}

// openPassive opens a passive-mode data listener bound to 127.0.0.1.
// Returns ip + port for PASV. The listener is consumed by the next data
// command (STOR/RETR/LIST), then closed.
func (s *session) openPassive(srv *Server) (string, int, error) {
	s.closeData()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, err
	}
	if s.protectData && srv.tlsConfig != nil {
		l = tls.NewListener(l, srv.tlsConfig)
	}
	addr := l.Addr().(*net.TCPAddr)
	s.dataMu.Lock()
	s.dataListener = l
	s.dataPort = addr.Port
	s.dataMu.Unlock()
	return "127.0.0.1", addr.Port, nil
}

// acceptStor accepts the data connection and ingests an upload.
func (s *session) acceptStor(fs *mockFS, p string) (int64, error) {
	conn, err := s.acceptData()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	folder, name := splitPath(p)
	if folder == "" {
		folder = "inbox"
	}
	key := s.user + "/" + folder + "/" + name
	fs.mu.Lock()
	fs.files[key] = &fileState{}
	fs.mu.Unlock()
	w := &writeHandle{persist: fs.persist}
	n, copyErr := io.Copy(w, conn)
	fs.mu.Lock()
	if st, ok := fs.files[key]; ok {
		st.size = n
		st.completedAt = time.Now()
		if fs.persist {
			st.content = w.buf
		}
	}
	fs.mu.Unlock()
	return n, copyErr
}

// streamRetr writes payload to the accepted data connection.
func (s *session) streamRetr(body []byte) error {
	conn, err := s.acceptData()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(body)
	return err
}

// streamList writes a UNIX-style listing for jlaffaye/ftp's parser.
func (s *session) streamList(entries []listEntry) error {
	conn, err := s.acceptData()
	if err != nil {
		return err
	}
	defer conn.Close()
	w := bufio.NewWriter(conn)
	for _, e := range entries {
		// "drwxr-xr-x 1 owner group <size> Jan 02 15:04 <name>" — minimal
		// LIST format every parser handles.
		mod := e.mod
		if mod.IsZero() {
			mod = time.Now()
		}
		ts := mod.Format("Jan 02 15:04")
		w.WriteString(fmt.Sprintf("-rw-r--r-- 1 mock mock %d %s %s\r\n", e.size, ts, e.name))
	}
	return w.Flush()
}

// acceptData accepts the queued passive listener and tears it down.
func (s *session) acceptData() (net.Conn, error) {
	s.dataMu.Lock()
	l := s.dataListener
	s.dataListener = nil
	s.dataMu.Unlock()
	if l == nil {
		return nil, errors.New("no passive listener")
	}
	defer l.Close()
	if t, ok := l.(*net.TCPListener); ok {
		t.SetDeadline(time.Now().Add(15 * time.Second))
	}
	return l.Accept()
}

// ----- filesystem (mirrors mocksftp's promote-on-list semantics) ---------

type fileState struct {
	size        int64
	completedAt time.Time
	trackID     string
	// content holds the uploaded bytes when the server is started with
	// PersistContent=true. Nil otherwise (saves memory on throughput
	// runs where bytes don't matter — same trick mocksftp uses). Routed
	// copies (inbox/outbox/sent) share the slice; the upload owner
	// never mutates after Close.
	content []byte
}

type mockFS struct {
	mu             sync.Mutex
	files          map[string]*fileState
	delay          time.Duration
	routes         map[string]string
	failUsers      map[string]bool
	persist        bool
	evictAfterRead bool
}

func newMockFS(delay time.Duration, routes map[string]string, fail map[string]bool) *mockFS {
	if routes == nil {
		routes = map[string]string{}
	}
	if fail == nil {
		fail = map[string]bool{}
	}
	return &mockFS{files: map[string]*fileState{}, delay: delay, routes: routes, failUsers: fail}
}

func splitPath(p string) (folder, name string) {
	p = strings.TrimPrefix(path.Clean(p), "/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 1 {
		return "inbox", parts[0]
	}
	return parts[0], parts[1]
}

type listEntry struct {
	name string
	size int64
	mod  time.Time
}

func (fs *mockFS) promoteAll() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	now := time.Now()
	type pending struct{ oldKey, user, base string }
	var ready []pending
	for k, st := range fs.files {
		if st == nil || st.completedAt.IsZero() {
			continue
		}
		parts := strings.SplitN(k, "/", 3)
		if len(parts) != 3 || parts[1] != "inbox" {
			continue
		}
		if strings.Contains(parts[2], "#") {
			continue
		}
		if now.Sub(st.completedAt) < fs.delay {
			continue
		}
		ready = append(ready, pending{oldKey: k, user: parts[0], base: parts[2]})
	}
	for _, p := range ready {
		st := fs.files[p.oldKey]
		tid := randHex(8)
		st.trackID = tid
		trackedName := p.base + "#" + tid
		fs.files[p.user+"/inbox/"+trackedName] = st
		delete(fs.files, p.oldKey)
		dst := fs.routes[p.user]
		if dst == "" {
			dst = p.user
		}
		// v0.19.9 — copies share the same content slice (immutable after
		// upload close), so inbox/outbox/sent count as one byte slice
		// in memory, not three. Mirrors mocksftp's promote pattern.
		fs.files[dst+"/outbox/"+trackedName] = &fileState{size: st.size, completedAt: now, trackID: tid, content: st.content}
		fs.files[p.user+"/sent/"+trackedName] = &fileState{size: st.size, completedAt: now, trackID: tid, content: st.content}
	}
}

func (fs *mockFS) listForUser(user, p string) []listEntry {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	folder, _ := splitPath(p)
	if folder == "" {
		folder = "inbox"
	}
	prefix := user + "/" + folder + "/"
	out := make([]listEntry, 0, 8)
	for k, st := range fs.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := strings.TrimPrefix(k, prefix)
		if strings.Contains(name, "/") {
			continue
		}
		out = append(out, listEntry{name: name, size: st.size, mod: st.completedAt})
	}
	return out
}

func (fs *mockFS) read(user, p string) ([]byte, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	folder, name := splitPath(p)
	if folder == "" {
		folder = "outbox"
	}
	key := user + "/" + folder + "/" + name
	st, ok := fs.files[key]
	if !ok {
		return nil, false
	}
	// v0.19.9 — byte-faithful replay when persist=on. Mirrors mocksftp.
	var body []byte
	if fs.persist && st.content != nil {
		body = st.content
	} else {
		// Synthesise zero-filled bytes of recorded size.
		body = make([]byte, st.size)
	}
	// v0.19.9 — evict-after-read. Drop the outbox entry plus the
	// matching source-side inbox + sent copies so memory is bounded by
	// in-flight files only. The body slice returned above keeps the
	// underlying bytes alive for the duration of the actual streamRetr
	// copy; once the caller is done writing to the wire and drops the
	// reference, GC reclaims the memory.
	if folder == "outbox" && fs.evictAfterRead {
		delete(fs.files, key)
		for src, dst := range fs.routes {
			if dst == user {
				delete(fs.files, src+"/inbox/"+name)
				delete(fs.files, src+"/sent/"+name)
			}
		}
	}
	return body, true
}

func (fs *mockFS) delete(user, p string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	folder, name := splitPath(p)
	key := user + "/" + folder + "/" + name
	if _, ok := fs.files[key]; !ok {
		return false
	}
	delete(fs.files, key)
	return true
}

func (fs *mockFS) rename(user, from, to string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ff, fn := splitPath(from)
	tf, tn := splitPath(to)
	src := user + "/" + ff + "/" + fn
	dst := user + "/" + tf + "/" + tn
	st, ok := fs.files[src]
	if !ok {
		return false
	}
	delete(fs.files, src)
	fs.files[dst] = st
	return true
}

func (fs *mockFS) size(user, p string) (int64, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	folder, name := splitPath(p)
	if st, ok := fs.files[user+"/"+folder+"/"+name]; ok {
		return st.size, true
	}
	return 0, false
}

// writeHandle counts bytes consumed during STOR. When persist is on,
// it ALSO buffers the bytes so the server can replay them on RETR
// (the byte-faithful path needed for hash-verify round trips).
type writeHandle struct {
	n       int64
	persist bool
	buf     []byte
}

func (w *writeHandle) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if w.persist {
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

// ----- helpers -----------------------------------------------------------

// buildSelfSignedCert returns DER + PEM cert + key. Uses caller-supplied
// PEM when both are non-empty (so tests can pin a known fingerprint),
// otherwise generates a fresh ECDSA-or-RSA cert valid for "127.0.0.1" /
// "localhost".
func buildSelfSignedCert(certIn, keyIn []byte) (der, certPEM, keyPEM []byte, err error) {
	if len(certIn) > 0 && len(keyIn) > 0 {
		block, _ := pem.Decode(certIn)
		if block == nil {
			return nil, nil, nil, errors.New("could not decode caller cert PEM")
		}
		return block.Bytes, certIn, keyIn, nil
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "mockftp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost", "127.0.0.1"},
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return der, certPEM, keyPEM, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ParsePairs / ParseFailUsers — convenience for binary wrappers.
func ParsePairs(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		p := strings.SplitN(strings.TrimSpace(kv), "=", 2)
		if len(p) == 2 && p[0] != "" && p[1] != "" {
			out[p[0]] = p[1]
		}
	}
	return out
}

func ParseFailUsers(s string) map[string]bool {
	out := map[string]bool{}
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out[u] = true
		}
	}
	return out
}

// PortFromAddr is a tiny helper for tests that need just the port number.
func PortFromAddr(a net.Addr) int {
	if a == nil {
		return 0
	}
	if t, ok := a.(*net.TCPAddr); ok {
		return t.Port
	}
	host, port, _ := net.SplitHostPort(a.String())
	_ = host
	p, _ := strconv.Atoi(port)
	return p
}
