package bastion

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func okCallback(string, ssh.PublicKey) error { return nil }

func TestOpen_RejectsMissingHost(t *testing.T) {
	_, err := Open(Config{User: "u", Pass: "p", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err == nil || !strings.Contains(err.Error(), "host is required") {
		t.Fatalf("expected host-required error, got %v", err)
	}
}

func TestOpen_RejectsMissingUser(t *testing.T) {
	_, err := Open(Config{Host: "127.0.0.1", Pass: "p", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err == nil || !strings.Contains(err.Error(), "user is required") {
		t.Fatalf("expected user-required error, got %v", err)
	}
}

func TestOpen_RejectsMissingHostKeyCallback(t *testing.T) {
	_, err := Open(Config{Host: "127.0.0.1", User: "u", Pass: "p"})
	if err == nil || !strings.Contains(err.Error(), "host-key callback") {
		t.Fatalf("expected callback-required error, got %v", err)
	}
}

func TestOpen_RejectsMissingAuth(t *testing.T) {
	_, err := Open(Config{Host: "127.0.0.1", User: "u", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err == nil || !strings.Contains(err.Error(), "no auth method") {
		t.Fatalf("expected no-auth error, got %v", err)
	}
}

func TestNilClient_DialerAndCloseAreSafe(t *testing.T) {
	var b *Client
	if err := b.Close(); err != nil {
		t.Errorf("Close on nil receiver: %v", err)
	}
	b2 := &Client{}
	dial := b2.Dialer()
	if _, err := dial("tcp", "irrelevant:0"); err == nil {
		t.Error("dialer on closed client should error")
	}
}
