package web

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SecurityHeaders adds a baseline of headers that the OWASP scanners check
// for. STS is only sent when TLS is in use (browsers ignore HSTS over HTTP
// anyway, but advertising it on plaintext is a downgrade-attack vector).
//
// CSP: script-src is now strict 'self' — every script in the bundle is a
// served file under /js/. Inline <script> blocks would break and that's the
// point: any future XSS that injects an inline script will be blocked by
// the browser. style-src keeps 'unsafe-inline' because the legacy markup
// still has style="..." attributes; those are far lower risk than script
// execution and removing them is a separate cosmetic refactor.
func SecurityHeaders(next http.Handler, tls bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'")
		if tls {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// bodySizeLimits caps the request body for each JSON endpoint. Limits are per
// endpoint so a config blob (with large user CSVs) gets generous headroom but
// a probe call doesn't.
var bodySizeLimits = map[string]int64{
	"/api/start":           2 << 20, // 2 MiB — accommodates large user CSV
	"/api/schedule":        2 << 20,
	"/api/probe":           8 << 10, // 8 KiB
	"/api/stop":            1 << 10, // 1 KiB (empty body in practice)
	"/api/schedule/cancel": 1 << 10,
	"/api/hostkeys/remove": 1 << 10,
	"/api/cluster/start":   4 << 20, // worker creds + config; can be large
	"/api/cluster/stop":    1 << 10,
}

// BodySizeLimit caps r.Body via http.MaxBytesReader before the handler reads
// it. For any path not in bodySizeLimits, a permissive default (1 MiB) is
// applied — protects unknown future endpoints from accidental unbounded reads.
func BodySizeLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			limit, ok := bodySizeLimits[r.URL.Path]
			if !ok {
				limit = 1 << 20
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// BasicAuth wraps next with HTTP Basic-auth using a constant-time comparison
// against the configured user/pass.
func BasicAuth(next http.Handler, user, pass string) http.Handler {
	expectedUser := []byte(user)
	expectedPass := []byte(pass)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow /healthz unauthenticated so probes/load balancers still
		// work. The verbose /healthz?detail=1 form is auth-gated so an
		// unauthenticated caller can't fingerprint uptime or detect that
		// a run is active.
		if r.URL.Path == "/healthz" && r.URL.Query().Get("detail") != "1" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="sftp-loadtest"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		userOK := subtle.ConstantTimeCompare([]byte(u), expectedUser) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), expectedPass) == 1
		if !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="sftp-loadtest"`)
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRFGuard requires the X-Requested-With header on every state-changing
// request. Browsers can't set custom headers cross-origin without a CORS
// preflight — and we don't reply to preflights — so a malicious page can't
// forge our POSTs even if the user is authenticated. Cheap, effective.
func CSRFGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Requested-With") != "sftp-loadtest" {
			http.Error(w, "missing X-Requested-With header (CSRF guard)", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitedPaths is the set of endpoints that get a per-IP token bucket.
// Two tiers: state-changing endpoints get a tight bucket (10 capacity, 1/s);
// read-only endpoints get a generous bucket (60 capacity, 30/s) — comfortably
// above the 2 s UI poll cadence but bounded so a malicious client can't
// generate unbounded load by spamming /api/runs or /api/host.
var rateLimitedPaths = map[string]rateLimit{
	"/api/start":           {capacity: 10, refill: 1.0},
	"/api/probe":           {capacity: 10, refill: 1.0},
	"/api/schedule":        {capacity: 10, refill: 1.0},
	"/api/schedule/cancel": {capacity: 10, refill: 1.0},
	"/api/stop":            {capacity: 10, refill: 1.0},
	"/api/hostkeys/remove": {capacity: 10, refill: 1.0},
	"/api/cluster/start":   {capacity: 5, refill: 0.5},
	"/api/cluster/stop":    {capacity: 10, refill: 1.0},
	"/api/runs":            {capacity: 60, refill: 30.0},
	"/api/host":            {capacity: 60, refill: 30.0},
	"/api/status":          {capacity: 60, refill: 30.0},
	"/api/schedules":       {capacity: 60, refill: 30.0},
	"/api/hostkeys":        {capacity: 60, refill: 30.0},
	"/api/cluster/status":  {capacity: 60, refill: 30.0},
}

type rateLimit struct {
	capacity float64
	refill   float64
}

// trustedProxies, when non-nil, is the only set of source-IP CIDRs whose
// X-Forwarded-For header is honoured for rate-limit attribution. When nil
// (default) the rate limiter ignores X-Forwarded-For entirely and uses the
// raw RemoteAddr — closing the spoof-bypass that earlier honoured the header
// unconditionally.
var trustedProxies []*net.IPNet

// SetTrustedProxies parses CIDR strings and configures which source IPs are
// allowed to forward client identity via X-Forwarded-For. Called once at
// startup from main.go's -trust-proxy flag. Empty slice clears any previous
// configuration.
func SetTrustedProxies(cidrs []string) error {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return fmt.Errorf("trust-proxy %q: %w", c, err)
		}
		out = append(out, n)
	}
	trustedProxies = out
	return nil
}

func sourceIPIsTrustedProxy(remoteAddr string) bool {
	if len(trustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

type tokenBucket struct {
	tokens   float64
	last     time.Time
	capacity float64
	refill   float64 // tokens per second
}

func (b *tokenBucket) allow() bool {
	now := time.Now()
	if !b.last.IsZero() {
		b.tokens += b.refill * now.Sub(b.last).Seconds()
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
	} else {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RateLimit applies a simple token-bucket per (client-IP, path) pair to the
// expensive endpoints. Defaults: capacity 10, refill 1 token/s — fast enough
// for legitimate UI use, slow enough to make /api/probe useless as a brute-
// force amplifier. Buckets are evicted lazily after 10 minutes of idleness so
// the map doesn't grow forever.
func RateLimit(next http.Handler) http.Handler {
	var (
		mu      sync.Mutex
		buckets = map[string]*tokenBucket{}
	)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for k, b := range buckets {
				if b.last.Before(cutoff) {
					delete(buckets, k)
				}
			}
			mu.Unlock()
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, limited := rateLimitedPaths[r.URL.Path]
		if !limited {
			next.ServeHTTP(w, r)
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		// X-Forwarded-For is only trusted when the request originates from
		// a configured trusted-proxy CIDR (-trust-proxy). Without that, any
		// caller on the network could spoof the header and bypass the
		// per-IP rate-limit by rotating fake values.
		if sourceIPIsTrustedProxy(r.RemoteAddr) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				// Use the LEFTMOST entry — the original client identity. The
				// trusted proxy appends its own IP to the right; we don't
				// want to attribute load to the proxy itself.
				if i := strings.IndexByte(xff, ','); i >= 0 {
					xff = xff[:i]
				}
				ip = strings.TrimSpace(xff)
			}
		}
		key := ip + "|" + r.URL.Path
		mu.Lock()
		b := buckets[key]
		if b == nil {
			b = &tokenBucket{capacity: limit.capacity, refill: limit.refill}
			buckets[key] = b
		}
		ok := b.allow()
		mu.Unlock()
		if !ok {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
