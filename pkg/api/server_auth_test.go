package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emon5122/dockwarden/internal/config"
)

const goodToken = "0123456789abcdef0123456789abcdef" // 32 chars

func newTestServer(token string) *Server {
	return NewServer(&config.Config{APIToken: token, LogLevel: "info"}, nil, nil, nil)
}

func do(s *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	return w
}

func TestValidateAPIToken(t *testing.T) {
	for _, tc := range []struct {
		token string
		ok    bool
	}{
		{"", false},
		{"short", false},
		{goodToken[:31], false},
		{goodToken, true},
	} {
		if err := ValidateAPIToken(tc.token); (err == nil) != tc.ok {
			t.Errorf("ValidateAPIToken(len %d) err=%v, want ok=%v", len(tc.token), err, tc.ok)
		}
	}
}

func TestStartRefusesWithoutStrongToken(t *testing.T) {
	for _, tok := range []string{"", "tooshort"} {
		if err := newTestServer(tok).Start(); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Fatalf("Start() with token len %d: err=%v, want refusal", len(tok), err)
		}
	}
}

// The audit finding: with no token, "Authorization: Bearer " (empty) got 200.
func TestAPIRejectsEmptyBearerWhenNoToken(t *testing.T) {
	s := newTestServer("")
	for _, h := range []string{"", "Bearer ", "Bearer"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		if w := do(s, req); w.Code != http.StatusUnauthorized {
			t.Errorf("no token, Authorization %q: got %d, want 401", h, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/containers/x/stop", nil)
	req.Header.Set("Authorization", "Bearer ")
	if w := do(s, req); w.Code != http.StatusUnauthorized {
		t.Errorf("no token, POST stop: got %d, want 401", w.Code)
	}
}

func TestAPIBearer(t *testing.T) {
	s := newTestServer(goodToken)
	req := httptest.NewRequest(http.MethodPost, "/v1/containers/x/stop", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if w := do(s, req); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong bearer: got %d, want 401", w.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("Authorization", "Bearer "+goodToken)
	if w := do(s, req); w.Code == http.StatusUnauthorized {
		t.Errorf("right bearer: got 401")
	}
}

func TestUIRequiresLoginWhenNoToken(t *testing.T) {
	s := newTestServer("")
	w := do(s, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("no token, GET /: got %d -> %q, want 302 -> /login", w.Code, w.Header().Get("Location"))
	}
	// An empty login must never create a session.
	form := url.Values{"token": {""}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = do(s, req)
	if loc := w.Header().Get("Location"); loc != "/login?error=invalid" {
		t.Errorf("empty login with no token: Location %q, want /login?error=invalid", loc)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "dockwarden_session" && c.Value != "" {
			t.Errorf("empty login set a session cookie")
		}
	}
}

func TestUILogin(t *testing.T) {
	s := newTestServer(goodToken)
	form := url.Values{"token": {goodToken}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := do(s, req)
	if w.Header().Get("Location") != "/" {
		t.Fatalf("good login: Location %q, want /", w.Header().Get("Location"))
	}
	var sess string
	for _, c := range w.Result().Cookies() {
		if c.Name == "dockwarden_session" {
			sess = c.Value
		}
	}
	if !s.isValidSession(sess) {
		t.Fatalf("good login: no valid session")
	}
}
