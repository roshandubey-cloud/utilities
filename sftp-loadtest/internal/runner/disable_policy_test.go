package runner

import (
	"testing"

	"github.com/roshandubey-cloud/utilities/sftp-loadtest/internal/config"
)

// TestDisablePolicy_ServerFeedbackDoesNotDisable pins the v0.19.13 fix:
// transport-layer codes returned by the server (broken pipe = WRITE, RETR
// failure = DOWNLOAD, etc.) must NOT trip the auto-disable threshold.
// Pre-fix, 5×WRITE from a single overloaded server retired every user
// at ~28 min into a 1 h stress run — silencing the very capacity-ceiling
// signal the load test was meant to capture.
func TestDisablePolicy_ServerFeedbackDoesNotDisable(t *testing.T) {
	users := []config.UserCreds{{Username: "u1"}}
	p := newDisablePolicy(5, users, nil, users)

	// 100 transport / server-feedback failures for upload AND download:
	// none of these codes describe an account-level problem, so none
	// should disable the user.
	for i := 0; i < 100; i++ {
		for _, code := range []string{"WRITE", "CLOSE", "CREATE", "TRACKID_TIMEOUT", "HASH_MISMATCH", "SOURCE", "UNKNOWN"} {
			p.onFailureFor("u1", "upload", code, "f.bin")
		}
		p.onFailureFor("u1", "download", "DOWNLOAD", "f.bin")
	}

	if p.isDisabled("u1", "upload") {
		t.Fatal("upload user disabled by server-feedback codes — load tester is silencing its own measurement")
	}
	if p.isDisabled("u1", "download") {
		t.Fatal("download user disabled by DOWNLOAD code — load tester is silencing its own measurement")
	}
}

// TestDisablePolicy_AccountFailuresDoDisable pins the other half: dial /
// auth failures (POOL_EMPTY) and runtime panics MUST still trip the
// threshold so a fundamentally broken account stops getting hammered.
func TestDisablePolicy_AccountFailuresDoDisable(t *testing.T) {
	users := []config.UserCreds{{Username: "u1"}}
	p := newDisablePolicy(3, users, nil, nil)

	for i := 0; i < 3; i++ {
		p.onFailureFor("u1", "upload", "POOL_EMPTY", "")
	}
	if !p.isDisabled("u1", "upload") {
		t.Fatal("3×POOL_EMPTY did not disable — auto-retire is dead, broken accounts will be hammered forever")
	}

	users2 := []config.UserCreds{{Username: "u2"}}
	p2 := newDisablePolicy(3, users2, nil, nil)
	for i := 0; i < 3; i++ {
		p2.onFailureFor("u2", "upload", "PANIC", "")
	}
	if !p2.isDisabled("u2", "upload") {
		t.Fatal("3×PANIC did not disable — runtime crash loop unprotected")
	}
}

// TestDisablePolicy_MixedDoesNotDilute pins that interleaved server-
// feedback failures don't reset the consecutive counter — 3 POOL_EMPTY
// should still disable even if WRITE errors land between them.
func TestDisablePolicy_MixedDoesNotDilute(t *testing.T) {
	users := []config.UserCreds{{Username: "u1"}}
	p := newDisablePolicy(3, users, nil, nil)

	p.onFailureFor("u1", "upload", "POOL_EMPTY", "")
	p.onFailureFor("u1", "upload", "WRITE", "f1") // server feedback, ignored by counter
	p.onFailureFor("u1", "upload", "POOL_EMPTY", "")
	p.onFailureFor("u1", "upload", "WRITE", "f2")
	p.onFailureFor("u1", "upload", "POOL_EMPTY", "")

	if !p.isDisabled("u1", "upload") {
		t.Fatal("3×POOL_EMPTY interleaved with WRITE didn't disable — server-feedback is wrongly resetting the account-level counter")
	}
}
