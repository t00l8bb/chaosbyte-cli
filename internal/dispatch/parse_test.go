package dispatch_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/bchayka/gitstatus/internal/dispatch"
)

func TestParseRun(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"/run ls", []string{"ls"}},
		{"/run ls -la", []string{"ls", "-la"}},
		{`/run echo "hello world"`, []string{"echo", "hello world"}},
		{`/run echo 'single quoted'`, []string{"echo", "single quoted"}},
		{`/run echo "she said \"hi\""`, []string{"echo", `she said "hi"`}},
		{"  /run  cargo   test  ", []string{"cargo", "test"}},
	}
	for _, tc := range cases {
		got, err := dispatch.Parse(tc.body)
		if err != nil {
			t.Errorf("Parse(%q) err: %v", tc.body, err)
			continue
		}
		if got.Verb != dispatch.VerbRun {
			t.Errorf("Parse(%q) Verb = %q, want run", tc.body, got.Verb)
		}
		if !reflect.DeepEqual(got.Argv, tc.want) {
			t.Errorf("Parse(%q) Argv = %v, want %v", tc.body, got.Argv, tc.want)
		}
	}
}

func TestParsePull(t *testing.T) {
	got, err := dispatch.Parse("/pull /tmp/foo.git")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verb != dispatch.VerbPull {
		t.Errorf("Verb = %q, want pull", got.Verb)
	}
	if got.Raw != "/tmp/foo.git" {
		t.Errorf("Raw = %q, want /tmp/foo.git", got.Raw)
	}
	if !reflect.DeepEqual(got.Argv, []string{"/tmp/foo.git"}) {
		t.Errorf("Argv = %v", got.Argv)
	}
}

func TestParsePullRequiresPath(t *testing.T) {
	if _, err := dispatch.Parse("/pull"); err == nil {
		t.Error("/pull without argument should error")
	}
	if _, err := dispatch.Parse("/pull   "); err == nil {
		t.Error("/pull with whitespace-only argument should error")
	}
}

func TestParseScratch(t *testing.T) {
	cases := []struct {
		body     string
		wantDesc string
	}{
		{"/scratch", ""},
		{"/scratch ", ""},
		{"/scratch a quick test", "a quick test"},
	}
	for _, tc := range cases {
		got, err := dispatch.Parse(tc.body)
		if err != nil {
			t.Errorf("Parse(%q) err: %v", tc.body, err)
			continue
		}
		if got.Verb != dispatch.VerbScratch {
			t.Errorf("Parse(%q) Verb = %q, want scratch", tc.body, got.Verb)
		}
		if got.Raw != tc.wantDesc {
			t.Errorf("Parse(%q) Raw = %q, want %q", tc.body, got.Raw, tc.wantDesc)
		}
	}
}

func TestParseSh(t *testing.T) {
	got, err := dispatch.Parse(`/sh echo hi | wc -l`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verb != dispatch.VerbSh {
		t.Errorf("Verb = %q, want sh", got.Verb)
	}
	wantArgv := []string{"/bin/sh", "-c", "echo hi | wc -l"}
	if !reflect.DeepEqual(got.Argv, wantArgv) {
		t.Errorf("Argv = %v, want %v", got.Argv, wantArgv)
	}
}

func TestParseRejectsNonCommands(t *testing.T) {
	cases := []string{
		"hello world",
		"// double slash",
		" not a command",
	}
	for _, body := range cases {
		_, err := dispatch.Parse(body)
		if !errors.Is(err, dispatch.ErrNotACommand) {
			t.Errorf("Parse(%q) err = %v, want ErrNotACommand", body, err)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []string{
		"/run",
		"/sh",
		"/run\nmultiline",
	}
	for _, body := range cases {
		if _, err := dispatch.Parse(body); err == nil {
			t.Errorf("Parse(%q) should error", body)
		}
	}
}

func TestParseUnknownVerbIsNotCommand(t *testing.T) {
	_, err := dispatch.Parse("/teleport home")
	if !errors.Is(err, dispatch.ErrNotACommand) {
		t.Errorf("Parse unknown verb err = %v, want ErrNotACommand", err)
	}
}
