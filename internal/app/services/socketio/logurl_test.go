package socketio

import "testing"

// The app log gets the scheme and host of a connection only: the path,
// query and userinfo may hold a secret variable's value.
func TestLogURL(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"https://api.example:8443/socket.io/?token=s3cret&EIO=4", "https://api.example:8443"},
		{"wss://user:pa55@host.example/ns?x=1", "wss://host.example"},
		{"http://host.example#frag", "http://host.example"},
		{"http://host.example", "http://host.example"},
		{"not a url", ""},
	}
	for _, tt := range tests {
		if got := logURL(tt.in); got != tt.want {
			t.Errorf("logURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
