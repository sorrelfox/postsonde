package oauth2

import (
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// A refresh sends grant_type=refresh_token and the refresh token, the
// client authenticating as for its grant; the scope and the grant's extra
// parameters are left out.
func TestRefreshRequest(t *testing.T) {
	t.Parallel()
	cfg := Config{TokenURL: "https://idp.test/t", ClientID: "app", ClientSecret: "s", Scope: "read", Audience: "https://api",
		Params: []Param{{"tenant", "t1"}}}
	tests := []struct {
		name       string
		clientAuth string
		wantBody   string
		wantBasic  bool
	}{
		{name: "header", wantBody: "grant_type=refresh_token&refresh_token=r+1%2F2", wantBasic: true},
		{name: "body", clientAuth: ClientAuthBody, wantBody: "grant_type=refresh_token&refresh_token=r+1%2F2&client_id=app&client_secret=s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := cfg
			c.ClientAuth = tt.clientAuth
			h := RefreshRequest(c, "r 1/2").HTTP(httpclient.Settings{})
			body, _ := h.Body.(httpclient.RawBody)
			if string(body.Data) != tt.wantBody {
				t.Errorf("body = %s, want %s", body.Data, tt.wantBody)
			}
			basic := false
			for _, hd := range h.Headers {
				basic = basic || (hd.Name == "Authorization" && strings.HasPrefix(hd.Value, "Basic "))
			}
			if basic != tt.wantBasic || h.URL != cfg.TokenURL || h.Method != "POST" || h.Settings.FollowRedirects {
				t.Errorf("request = %+v", h)
			}
		})
	}
	if len(cfg.Params) != 1 {
		t.Error("RefreshRequest changed the caller's parameters")
	}
}

// A refresh against the test identity provider: its answer is a token
// like any other.
func TestExchangeRefresh(t *testing.T) {
	t.Parallel()
	idp := newIDP(t)
	idp.reply = func() (int, string, string) {
		return 200, "application/json", `{"access_token":"new","token_type":"Bearer","expires_in":60}`
	}
	res, err := Exchange(t.Context(), client(), RefreshRequest(Config{TokenURL: idp.URL, ClientID: "app", ClientSecret: idp.secret}, "rt"), httpclient.Settings{}, nil)
	if err != nil || res.Token.AccessToken != "new" || res.Token.RefreshToken != "" {
		t.Fatalf("Exchange = %+v, %v", res, err)
	}
	idp.mu.Lock()
	defer idp.mu.Unlock()
	if f := idp.forms[0]; f.Get("grant_type") != GrantRefreshToken || f.Get("refresh_token") != "rt" {
		t.Errorf("form = %v", f)
	}
}
