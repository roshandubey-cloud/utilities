package patterns

import (
	"testing"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

func TestBuiltinHikariMatches(t *testing.T) {
	reg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	rules := reg.Rules()
	if len(rules) < 3 {
		t.Errorf("expected at least 3 builtin rules, got %d", len(rules))
	}

	// Synthesise a dump with 3 threads parked in HikariPool.getConnection.
	d := &parser.Dump{}
	for i := 0; i < 3; i++ {
		d.Threads = append(d.Threads, parser.Thread{
			Name:  "http-nio-8080-exec-X",
			State: parser.StateWaiting,
			Frames: []parser.Frame{
				{Class: "com.zaxxer.hikari.pool.HikariPool", Method: "getConnection", Source: "HikariPool.java:151"},
			},
		})
	}
	matches := reg.Apply(d)
	hit := false
	for _, m := range matches {
		if m.ID == "hikari-pool-saturated" {
			hit = true
			if len(m.Threads) != 3 {
				t.Errorf("hikari match threads=%d", len(m.Threads))
			}
		}
	}
	if !hit {
		t.Errorf("expected hikari-pool-saturated to match, got: %+v", matches)
	}
}

func TestTomcatHangPatternMatches(t *testing.T) {
	reg, _ := Load("")
	d := &parser.Dump{}
	for i := 0; i < 5; i++ {
		d.Threads = append(d.Threads, parser.Thread{
			Name: "http-nio-8080-exec-X",
			Frames: []parser.Frame{
				{Class: "sun.nio.ch.SocketChannelImpl", Method: "read", Source: "SocketChannelImpl.java:295"},
				{Class: "com.acme.OrderService", Method: "placeOrder", Source: "OrderService.kt:148"},
			},
		})
	}
	matches := reg.Apply(d)
	hit := false
	for _, m := range matches {
		if m.ID == "tomcat-worker-stuck-on-io" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected tomcat-worker-stuck-on-io to match")
	}
}
