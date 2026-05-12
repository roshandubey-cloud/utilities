// Package bastion implements single-hop SSH ProxyJump for the SFTP load
// runner. v0.16.0 introduced this so operators can drive load tests
// against targets that aren't directly reachable from the load host —
// the typical case for production SFTP endpoints sitting behind a
// jump host / bastion in a locked-down VPC.
//
// One bastion SSH session is opened up front (Open) and reused as the
// transport for every per-user dial. The runner closes the bastion
// when the run ends. SFTP-only — FTP/FTPS through bastion is
// uncommon enough that we left it out of v0.16.
package bastion

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/sftpx"
)

// Config is the bastion-side connection profile. Mirrors the target
// auth surface — a bastion can use a password or its own private key
// (independent from the target's), with an optional passphrase.
type Config struct {
	Host       string
	Port       int
	User       string
	Pass       string
	PrivateKey []byte // PEM-encoded; empty means "use Pass"
	Passphrase string

	// HostKeyCallback is the bastion's host-key verifier. The runner
	// passes the same store-backed callback the target uses, so a
	// bastion's first contact gets TOFU'd into the host-key store
	// alongside target keys (one persistent file, two keyed entries).
	HostKeyCallback ssh.HostKeyCallback
}

// keepaliveInterval is how often the bastion sends an out-of-band
// keepalive request so middleboxes / NAT timers don't silently
// idle-close the single TCP that every forwarded target channel
// rides. Mirrors the target sftpx.Client cadence — corporate
// network proxies typically idle-close at 5–15 min, so 30 s is
// well inside the safety margin.
const keepaliveInterval = 30 * time.Second

// Client is an opened bastion SSH session. Dial wires its
// ssh.Client.Dial as the underlying transport for SFTP target dials;
// Close terminates the bastion session (target dials done through it
// continue working until they Close themselves — Go's ssh stack
// multiplexes channels over the single TCP).
type Client struct {
	c      *ssh.Client
	stopCh chan struct{}
}

// Open dials the bastion and authenticates. Returns a Client that can
// be used to Dial through to a target. The caller owns Close().
func Open(cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("bastion: host is required")
	}
	if cfg.Port <= 0 {
		cfg.Port = 22
	}
	if cfg.User == "" {
		return nil, errors.New("bastion: user is required")
	}
	if cfg.HostKeyCallback == nil {
		return nil, errors.New("bastion: host-key callback is required")
	}

	var auth []ssh.AuthMethod
	if len(cfg.PrivateKey) > 0 {
		signer, err := sftpx.ParsePrivateKey(cfg.PrivateKey, cfg.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("bastion: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Pass != "" {
		// v0.20.8 — append BOTH password and keyboard-interactive
		// (answering each KI prompt with the same password) so
		// enterprise SSH gateways that advertise only KI on the wire
		// (e.g. MoveIT Transfer) authenticate the same as they do
		// with any third-party SFTP client.
		auth = append(auth, sftpx.PasswordAuthMethods(cfg.Pass)...)
	}
	if len(auth) == 0 {
		return nil, errors.New("bastion: no auth method (provide pass or private key)")
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: cfg.HostKeyCallback,
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("bastion dial %s: %w", addr, err)
	}
	bc := &Client{c: c, stopCh: make(chan struct{})}
	go bc.keepalive()
	return bc, nil
}

// keepalive sends out-of-band keepalive requests every keepaliveInterval
// so the bastion's single TCP doesn't get silently closed by NAT
// idle-timeout or a middlebox while the runner is between dials. Exits
// the first time the server fails to reply — every forwarded channel
// will then EOF on next read and the runner will see surfaced errors
// instead of hanging on a half-open connection.
func (b *Client) keepalive() {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-t.C:
			if b == nil || b.c == nil {
				return
			}
			if _, _, err := b.c.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// Dialer returns a function with the same signature as net.Dial that
// routes through this bastion's SSH session. The returned function is
// safe for concurrent use — ssh.Client.Dial multiplexes onto channels
// of the single underlying TCP, so per-user pool slots can each open
// their own forwarded TCP connection without serialising.
func (b *Client) Dialer() func(network, addr string) (net.Conn, error) {
	return func(network, addr string) (net.Conn, error) {
		if b == nil || b.c == nil {
			return nil, errors.New("bastion: client is closed")
		}
		return b.c.Dial(network, addr)
	}
}

// Close terminates the bastion SSH session. Forwarded channels still
// in use will receive EOF on their next read. Stops the keepalive
// goroutine. Idempotent — safe to call from teardown paths that may
// double-fire on cancel + run-end.
func (b *Client) Close() error {
	if b == nil || b.c == nil {
		return nil
	}
	if b.stopCh != nil {
		select {
		case <-b.stopCh:
			// already closed
		default:
			close(b.stopCh)
		}
	}
	err := b.c.Close()
	b.c = nil
	return err
}
