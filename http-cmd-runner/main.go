// Command http-cmd-runner is a small HTTP wrapper that executes Linux commands
// and scripts on the host it runs on. There is NO authentication — anyone who
// can reach the listen address can run commands as the service user. Bind it to
// 127.0.0.1 and keep it off untrusted networks.
//
// Two execution modes, chosen in config:
//   - allowlist (default): only pre-approved programs run, executed directly
//     without a shell, so there is no shell-injection surface.
//   - arbitrary: any shell line runs via "sh -c". Powerful and dangerous;
//     enable only on trusted, network-isolated hosts.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.json", "path to JSON config file (use \"\" to rely on defaults + env)")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatalf("open log file %q: %v", cfg.LogFile, err)
		}
		defer f.Close()
		logger = log.New(f, "", log.LstdFlags|log.LUTC)
	}

	srv := &server{cfg: cfg, log: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.handleHealth)
	mux.HandleFunc("/exec", srv.handleExec)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		mode := "allowlist"
		if cfg.AllowArbitrary {
			mode = "ARBITRARY (any shell command)"
		}
		logger.Printf("listening on %s | mode=%s | tls=%t", cfg.Listen, mode, cfg.TLSCertFile != "")
		var err error
		if cfg.TLSCertFile != "" {
			err = httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Printf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

type server struct {
	cfg Config
	log *log.Logger
}

// execRequest is the JSON body accepted by POST /exec.
type execRequest struct {
	// Command is the program to run (allowlist mode) or the full shell line
	// (arbitrary mode).
	Command string `json:"command"`
	// Args are passed to the program in allowlist mode. Ignored in arbitrary mode.
	Args []string `json:"args"`
	// Stdin is fed to the process standard input.
	Stdin string `json:"stdin"`
	// TimeoutSec overrides the default timeout, capped by max_timeout_sec.
	TimeoutSec int `json:"timeout_sec"`
}

// execResponse is the JSON body returned by POST /exec.
type execResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	var req execRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 5<<20)) // 5 MiB body cap
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	timeout := s.cfg.DefaultTimeoutSec
	if req.TimeoutSec > 0 {
		timeout = req.TimeoutSec
	}
	if timeout > s.cfg.MaxTimeoutSec {
		timeout = s.cfg.MaxTimeoutSec
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd, err := s.buildCmd(ctx, req)
	if err != nil {
		s.log.Printf("rejected from %s: %v", r.RemoteAddr, err)
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	var stdout, stderr cappedBuffer
	stdout.limit = s.cfg.MaxOutputBytes
	stderr.limit = s.cfg.MaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	s.log.Printf("exec from %s: %q args=%v timeout=%ds", r.RemoteAddr, req.Command, req.Args, timeout)
	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)

	resp := execResponse{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: dur.Milliseconds(),
		TimedOut:   ctx.Err() == context.DeadlineExceeded,
		Truncated:  stdout.truncated || stderr.truncated,
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		resp.ExitCode = 0
	case errors.As(runErr, &exitErr):
		resp.ExitCode = exitErr.ExitCode()
	default:
		// Could not start the process at all (not found, perm denied, timeout).
		resp.ExitCode = -1
		if resp.Stderr == "" {
			resp.Stderr = runErr.Error()
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// buildCmd constructs the *exec.Cmd according to the configured mode, enforcing
// the allowlist when arbitrary execution is disabled.
func (s *server) buildCmd(ctx context.Context, req execRequest) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if s.cfg.AllowArbitrary {
		// Run the whole command line through a shell.
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
	} else {
		if !s.cfg.allows(req.Command) {
			return nil, errors.New("command not in allowlist: " + req.Command)
		}
		cmd = exec.CommandContext(ctx, req.Command, req.Args...)
	}
	if s.cfg.WorkingDir != "" {
		cmd.Dir = s.cfg.WorkingDir
	}
	return cmd, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// cappedBuffer is a bytes.Buffer that stops growing past limit and records that
// it truncated, so a runaway command cannot exhaust memory.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.limit > 0 {
		remaining := c.limit - c.buf.Len()
		if remaining <= 0 {
			c.truncated = true
			return len(p), nil // discard but report success so the process keeps running
		}
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
			c.truncated = true
			return len(p), nil
		}
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
