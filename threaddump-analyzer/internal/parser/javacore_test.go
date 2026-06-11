package parser

import (
	"strings"
	"testing"
)

const javacoreSample = `1TIDATETIME    Date: 2026/04/28 at 12:34:56:789
NULL           ------------------------------------------------------------------------
0SECTION       THREADS subcomponent dump routine
NULL           =================================
NULL
3XMTHREADINFO      "Thread-A" J9VMThread:0x000000000054EE00, omrthread_t:0x0000000000550000, java/lang/Thread:0x0000000000600000, state:B, prio=5
3XMTHREADINFO1            (native thread ID:0xB7A3, native priority:0x5, native policy:UNKNOWN, vmstate:B, vm thread flags:0x00000020)
3XMCPUTIME               CPU usage total: 0.012 secs, current category="Application"
3XMTHREADBLOCK     Blocked on: java/lang/Object@0x000000000ABC1234 Owned by: "Thread-B" (J9VMThread:0x000000000054EF00)
3XMTHREADINFO3           Java callstack:
4XESTACKTRACE                at com/acme/Demo.lambda$threadA$0(Demo.java:31)
4XESTACKTRACE                at java/lang/Thread.run(Thread.java:840)
NULL

3XMTHREADINFO      "Thread-B" J9VMThread:0x000000000054EF00, omrthread_t:0x0000000000550100
3XMTHREADINFO1            (native thread ID:0xB7A4, native priority:0x5, native policy:UNKNOWN, vmstate:B, vm thread flags:0x00000020)
3XMCPUTIME               CPU usage total: 0.011 secs, current category="Application"
3XMTHREADBLOCK     Blocked on: java/lang/Object@0x000000000ABC4321 Owned by: "Thread-A" (J9VMThread:0x000000000054EE00)
3XMTHREADINFO3           Java callstack:
4XESTACKTRACE                at com/acme/Demo.lambda$threadB$1(Demo.java:42)
4XESTACKTRACE                at java/lang/Thread.run(Thread.java:840)
NULL
`

func TestParseJavacoreBasic(t *testing.T) {
	d, err := ParseJavacore(strings.NewReader(javacoreSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(d.Threads))
	}
	if d.Threads[0].Name != "Thread-A" {
		t.Errorf("name[0]=%q", d.Threads[0].Name)
	}
	if d.Threads[0].State != StateBlocked {
		t.Errorf("state[0]=%v want BLOCKED", d.Threads[0].State)
	}
	if len(d.Threads[0].Frames) != 2 {
		t.Errorf("frames[0]=%d, want 2", len(d.Threads[0].Frames))
	}
	if got := d.Threads[0].Frames[0].Class; got != "com.acme.Demo" {
		t.Errorf("frame[0].class=%q, want com.acme.Demo", got)
	}
	wo := d.Threads[0].WaitingOn()
	if wo.ID == "" {
		t.Errorf("expected WaitingOn lock on Thread-A")
	}
	if wo.Class != "java.lang.Object" {
		t.Errorf("waiting-on class=%q", wo.Class)
	}
}

func TestParseAutoDispatches(t *testing.T) {
	d, err := ParseAuto(strings.NewReader(javacoreSample))
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	if len(d.Threads) != 2 {
		t.Errorf("ParseAuto on javacore got %d threads", len(d.Threads))
	}

	// HotSpot smoke test via the bundled sample is in analyzer_test.go;
	// here we just confirm Detect doesn't misroute a HotSpot dump.
	hotspot := `Full thread dump OpenJDK 64-Bit Server VM (17.0.10+9 mixed mode, sharing):

"main" #1 prio=5 os_prio=0 tid=0x1 nid=0x2 waiting on condition [0x3]
   java.lang.Thread.State: WAITING
	at java.lang.Object.wait(Native Method)
`
	if got := Detect([]byte(hotspot)); got != "jstack" {
		t.Errorf("Detect(hotspot)=%q, want jstack", got)
	}
}
