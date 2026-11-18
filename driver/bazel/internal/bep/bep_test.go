package bep

import (
	"strings"
	"testing"
)

func TestAnInvocationOfThreeTargets(t *testing.T) {
	inv, err := ReadFile("testdata/mixed.json")
	if err != nil {
		t.Fatal(err)
	}

	if !inv.Complete {
		t.Error("the file holds its last message")
	}

	pass, fail := inv.Results["//fx/pass:pass_test"], inv.Results["//fx/fail:fail_test"]
	if !pass.Passed() || pass.Cached || len(pass.Parts) != 1 ||
		!strings.HasSuffix(pass.Parts[0].Log, "/testlogs/fx/pass/pass_test/test.log") || !strings.HasSuffix(pass.Parts[0].XML, "/test.xml") {
		t.Errorf("pass %+v", pass)
	}

	if fail.Passed() || fail.Status != "FAILED" || fail.Seconds <= 0 {
		t.Errorf("fail %+v", fail)
	}

	if why := inv.Failed["//fx/broken:broken_test"]; !strings.Contains(why, "GoCompilePkg") {
		t.Errorf("broken %q", why)
	}

	if _, ok := inv.Results["//fx/broken:broken_test"]; ok {
		t.Error("a target that didn't build has no test result")
	}
}

func TestACutFile(t *testing.T) {
	in := `{"id":{"testResult":{"label":"//a:a_test","shard":1,"run":1,"attempt":1}},"testResult":{"status":"PASSED","cachedLocally":true,"testActionOutput":[{"name":"test.log","uri":"file:///o/a/test.log"},{"name":"test.lcov","uri":"bytestream://cache/blobs/1/2"}]}}
{"id":{"testSummary":{"label":"//a:a_test"}},"testSummary":{"overallStatus":"PASSED"}}
{"id":{"testResult":{"label":"//b:b_test","shard":1,"run":1,"attempt":1}},"testResult":{"status":"PASSED"}}
{"id":{"testResult":{"label":"//c:c_te`

	inv, err := Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}

	a := inv.Results["//a:a_test"]
	if inv.Complete || len(inv.Results) != 1 || !a.Cached || a.Parts[0].Log != "/o/a/test.log" || a.Parts[0].LCOV != "" || len(a.Parts[0].Remote) != 1 {
		t.Errorf("%+v", inv)
	}
}

func TestShardsAndRetries(t *testing.T) {
	in := `{"id":{"testResult":{"label":"//a:a_test","shard":1,"run":1,"attempt":1}},"testResult":{"status":"FAILED","testActionOutput":[{"name":"test.log","uri":"file:///o/1/test.log"}]}}
{"id":{"testResult":{"label":"//a:a_test","shard":2,"run":1,"attempt":1}},"testResult":{"status":"PASSED","testActionOutput":[{"name":"test.log","uri":"file:///o/2/test.log"}]}}
{"id":{"testSummary":{"label":"//a:a_test"}},"testSummary":{"overallStatus":"FAILED"}}
{"id":{"testResult":{"label":"//b:b_test","shard":1,"run":1,"attempt":1}},"testResult":{"status":"FAILED","testActionOutput":[{"name":"test.lcov","uri":"file:///o/b1/coverage.dat"}]}}
{"id":{"testResult":{"label":"//b:b_test","shard":1,"run":1,"attempt":2}},"testResult":{"status":"PASSED","testActionOutput":[{"name":"test.lcov","uri":"file:///o/b2/coverage.dat"}]}}
{"id":{"testSummary":{"label":"//b:b_test"}},"testSummary":{"overallStatus":"FLAKY"}}
{"id":{},"lastMessage":true}`

	inv, err := Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}

	a, b := inv.Results["//a:a_test"], inv.Results["//b:b_test"]
	if a.Passed() || len(a.Parts) != 2 || a.Parts[0].Log != "/o/1/test.log" || a.Parts[1].Status != "PASSED" {
		t.Errorf("shards %+v", a)
	}

	if !b.Passed() || len(b.Parts) != 1 || b.Parts[0].Attempt != 2 || b.Parts[0].LCOV != "/o/b2/coverage.dat" {
		t.Errorf("retries %+v", b)
	}
}

func TestAnInterruptedTargetIsNoBuildFailure(t *testing.T) {
	in := `{"id":{"targetCompleted":{"label":"//a:a_test"}},"aborted":{"reason":"USER_INTERRUPTED","description":"build interrupted"}}
{"id":{"targetCompleted":{"label":"//b:b_test"}},"aborted":{"reason":"ANALYSIS_FAILURE","description":"no such target"}}
{"id":{},"lastMessage":true}`

	inv, err := Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := inv.Failed["//a:a_test"]; ok || inv.Failed["//b:b_test"] == "" || !inv.Complete {
		t.Errorf("%+v", inv)
	}
}
