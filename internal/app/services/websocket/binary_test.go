package websocket

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestParseBase64(t *testing.T) {
	t.Parallel()
	ok := []struct {
		in   string
		want []byte
	}{
		{"", []byte{}},
		{"  \n", []byte{}},
		{"3q2+7w==", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"3q2+7w", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"3q2-7w==", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"3q2-\n7w=\n= ", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"AAEC", []byte{0, 1, 2}},
		{"AAE", []byte{0, 1}},
		{"_w==", []byte{0xff}},
		{"/w", []byte{0xff}},
	}
	for _, tc := range ok {
		got, err := parseBase64(tc.in)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("parseBase64(%q) = %x, %v; want %x", tc.in, got, err, tc.want)
		}
	}

	bad := []struct {
		in     string
		offset string
	}{
		{"3q2*7w", "3"},
		{"ab é", "3"},
		{"3q2+7w==x", "6"},
		{"3q2+7w===", "8"},
		{"3q2+7", "4"},
		{"3q2+7w=", "6"},
		{"=", "0"},
		{"AA\x01", "2"},
	}
	for _, tc := range bad {
		_, err := parseBase64(tc.in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidBase64 || e.Details["offset"] != tc.offset {
			t.Errorf("parseBase64(%q) = %v, want offset %s", tc.in, err, tc.offset)
		}
	}
	if err := checkFormat(""); err != nil {
		t.Error(err)
	}
}
