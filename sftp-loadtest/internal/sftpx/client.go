package sftpx

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// keepaliveInterval is how often we send an out-of-band keepalive request
// to the SSH server. 30 s is comfortably below every common idle-timeout
// (sshd defaults to 120 s, load balancers often enforce 60 s) but far enough
// apart that it's not meaningful traffic.
const keepaliveInterval = 30 * time.Second

type Client struct {
	ssh      *ssh.Client
	sftp     *sftp.Client
	stopCh   chan struct{}
	closeOnce sync.Once
}

func Dial(host string, port int, user, pass string) (*Client, error) {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	sshc, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	// Use pkg/sftp defaults everywhere — no packet-size tuning, no concurrency
	// overrides. Servers that advertise a 32 KiB max-packet limit drop connections
	// when we push bigger frames, so staying on defaults is the compatible choice.
	sc, err := sftp.NewClient(sshc)
	if err != nil {
		sshc.Close()
		return nil, fmt.Errorf("sftp open: %w", err)
	}
	c := &Client{ssh: sshc, sftp: sc, stopCh: make(chan struct{})}
	go c.keepalive()
	return c, nil
}

// keepalive sends SSH out-of-band keepalive requests every keepaliveInterval
// so servers + middleboxes don't silently idle-close the connection during
// long load tests. Exits the first time the server fails to reply — the next
// operation on this Client will surface the real error, and the pool's
// reconnect path will redial.
func (c *Client) keepalive() {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
			// Out-of-band global request; payload intentionally empty.
			if _, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// Upload streams src to the remote path. The returned stage pinpoints where
// a failure happened (empty string on success): "create" for the initial
// open, "write" for mid-stream I/O, "close" for a failed flush/close. Lets
// callers attach a stable ErrorCode to the failure record.
func (c *Client) Upload(remotePath string, src io.Reader) (int64, string, error) {
	f, err := c.sftp.Create(remotePath)
	if err != nil {
		return 0, "create", fmt.Errorf("create %s: %w", remotePath, err)
	}
	n, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return n, "write", copyErr
	}
	if closeErr != nil {
		return n, "close", closeErr
	}
	return n, "", nil
}

func (c *Client) List(remotePath string) ([]os.FileInfo, error) {
	return c.sftp.ReadDir(remotePath)
}

// Download streams a remote file into io.Discard. No local storage — we only
// care about byte count and timing for speed measurement.
func (c *Client) Download(remotePath string) (int64, error) {
	f, err := c.sftp.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", remotePath, err)
	}
	defer f.Close()
	return io.Copy(io.Discard, f)
}

// Close tears down the keepalive goroutine and the SSH/SFTP connections.
// Idempotent via sync.Once so concurrent closes from pool teardown + caller
// paths don't double-close the ssh conn (which pkg/ssh tolerates, but it's
// nicer to not rely on that).
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.stopCh)
		if c.sftp != nil {
			c.sftp.Close()
		}
		if c.ssh != nil {
			err = c.ssh.Close()
		}
	})
	return err
}
