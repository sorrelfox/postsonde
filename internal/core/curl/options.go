package curl

import (
	_ "embed"
	"strings"
)

// optionsTxt lists curl's options; see the header of the file.
//
//go:embed options.txt
var optionsTxt string

// optionSpec is one curl option.
type optionSpec struct {
	long  string // without "--"
	short byte   // 0 when the option has no short form
	arg   bool   // takes a value
}

var (
	longOptions  = map[string]*optionSpec{}
	shortOptions = map[byte]*optionSpec{}
)

func init() {
	for line := range strings.SplitSeq(optionsTxt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		spec := &optionSpec{}
		for f := range strings.FieldsSeq(line) {
			switch {
			case f == "<arg>":
				spec.arg = true
			case strings.HasPrefix(f, "--"):
				spec.long = f[2:]
			case len(f) == 2 && f[0] == '-':
				spec.short = f[1]
			default:
				panic("curl: bad line in options.txt: " + line)
			}
		}
		if spec.long == "" || longOptions[spec.long] != nil {
			panic("curl: bad line in options.txt: " + line)
		}
		longOptions[spec.long] = spec
		if spec.short != 0 {
			shortOptions[spec.short] = spec
		}
	}
}

// lookupLong finds a long option by name. A boolean option can be negated
// as --no-<name>; negated is then true.
func lookupLong(name string) (spec *optionSpec, negated bool) {
	if spec := longOptions[name]; spec != nil {
		return spec, false
	}
	if base, ok := strings.CutPrefix(name, "no-"); ok {
		if spec := longOptions[base]; spec != nil && !spec.arg {
			return spec, true
		}
	}
	return nil, false
}

// ignoredOptions do not change the request: output, progress, verbosity,
// retries and transfer limits. They are accepted with an info diagnostic.
var ignoredOptions = map[string]bool{
	"silent": true, "show-error": true, "verbose": true, "include": true,
	"output": true, "output-dir": true, "remote-name": true, "remote-name-all": true,
	"remote-header-name": true, "remote-time": true, "create-dirs": true,
	"progress-bar": true, "no-progress-meter": true, "write-out": true,
	"dump-header": true, "trace": true, "trace-ascii": true, "trace-time": true,
	"trace-ids": true, "trace-config": true, "stderr": true, "styled-output": true,
	"fail": true, "fail-with-body": true, "fail-early": true, "no-buffer": true,
	"xattr": true, "no-clobber": true, "remove-on-error": true, "skip-existing": true,
	"out-null": true, "libcurl": true, "help": true, "manual": true,
	"version": true, "retry": true, "retry-delay": true, "retry-max-time": true,
	"retry-all-errors": true, "retry-connrefused": true, "limit-rate": true,
	"speed-limit": true, "speed-time": true, "max-filesize": true, "parallel": true,
	"parallel-immediate": true, "parallel-max": true, "parallel-max-host": true,
	"globoff": true, "cookie-jar": true, "tcp-nodelay": true, "tcp-fastopen": true,
	"keepalive-time": true, "no-keepalive": true, "keepalive-cnt": true,
	"disable": true, "rate": true, "etag-save": true, "junk-session-cookies": true,
}

// looksLikeURL is the heuristic that decides whether the word after an
// unknown option is its value: a word that looks like a URL or a host is
// not taken.
func looksLikeURL(s string) bool {
	if strings.Contains(s, "://") || strings.HasPrefix(s, "localhost") {
		return true
	}
	host := s
	if k := strings.IndexAny(host, "/?#"); k >= 0 {
		host = host[:k]
	}
	if k := strings.LastIndexByte(host, ':'); k >= 0 {
		host = host[:k]
	}
	dot := strings.LastIndexByte(host, '.')
	if dot <= 0 || dot == len(host)-1 {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(isNameChar(c) || c == '.' || c == '-') {
			return false
		}
	}
	return true
}
