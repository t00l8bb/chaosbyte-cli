package dispatch

import (
	"bytes"
	"testing"
)

// TestSplitAtSafeBoundary covers the chunker's safe-split logic for
// the cases that matter to terminal output: plain ASCII, complete CSI
// sequences, complete OSC sequences, partial trailing CSI / UTF-8,
// and newline-driven flushes.
func TestSplitAtSafeBoundary(t *testing.T) {
	cases := []struct {
		name       string
		input      []byte
		wantEmit   []byte
		wantHold   []byte
	}{
		{
			name:     "empty",
			input:    nil,
			wantEmit: nil,
			wantHold: nil,
		},
		{
			name:     "plain ascii",
			input:    []byte("hello"),
			wantEmit: []byte("hello"),
			wantHold: []byte{},
		},
		{
			name:     "complete csi sequence",
			input:    []byte("\x1b[31mred\x1b[0m"),
			wantEmit: []byte("\x1b[31mred\x1b[0m"),
			wantHold: []byte{},
		},
		{
			name:     "partial csi at end is held",
			input:    []byte("hello\x1b[3"),
			wantEmit: []byte("hello"),
			wantHold: []byte("\x1b[3"),
		},
		{
			name:     "partial utf8 at end is held",
			input:    []byte{'a', 'b', 'c', 0xe2, 0x9c}, // missing 3rd byte of ✓
			wantEmit: []byte("abc"),
			wantHold: []byte{0xe2, 0x9c},
		},
		{
			name:     "complete utf8 emoji",
			input:    []byte("ok ✓"),
			wantEmit: []byte("ok ✓"),
			wantHold: []byte{},
		},
		{
			name:     "newline flushes early",
			input:    []byte("first\nsecond\x1b[3"),
			wantEmit: []byte("first\nsecond"),
			wantHold: []byte("\x1b[3"),
		},
		{
			name:     "osc with bel terminator",
			input:    []byte("\x1b]0;title\x07after"),
			wantEmit: []byte("\x1b]0;title\x07after"),
			wantHold: []byte{},
		},
		{
			name:     "osc partial without terminator is held",
			input:    []byte("\x1b]0;title"),
			wantEmit: nil,
			wantHold: []byte("\x1b]0;title"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, hold := splitAtSafeBoundary(tc.input)
			if !bytes.Equal(emit, tc.wantEmit) {
				t.Errorf("emit = %q, want %q", emit, tc.wantEmit)
			}
			if !bytes.Equal(hold, tc.wantHold) {
				t.Errorf("hold = %q, want %q", hold, tc.wantHold)
			}
		})
	}
}

func TestFindEscapeEnd(t *testing.T) {
	cases := []struct {
		name    string
		input   []byte
		wantLen int
		wantOK  bool
	}{
		{"csi short", []byte("\x1b[m"), 3, true},
		{"csi with params", []byte("\x1b[31;1m"), 7, true},
		{"csi incomplete", []byte("\x1b[31"), 0, false},
		{"osc bel", []byte("\x1b]0;t\x07"), 6, true},
		{"osc st", []byte("\x1b]0;t\x1b\\"), 7, true},
		{"osc incomplete", []byte("\x1b]0;t"), 0, false},
		{"simple esc", []byte("\x1b="), 2, true},
		{"charset designation", []byte("\x1b(B"), 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := findEscapeEnd(tc.input)
			if n != tc.wantLen || ok != tc.wantOK {
				t.Errorf("findEscapeEnd(%q) = (%d, %v), want (%d, %v)", tc.input, n, ok, tc.wantLen, tc.wantOK)
			}
		})
	}
}
