package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naturalmoods/aipocket"
	"github.com/naturalmoods/aipocket/internal/core"
)

func webRequest(h http.Handler, method, target, origin string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:8787"
	r.AddCookie(&http.Cookie{Name: "aipocket_web", Value: "test-web-token"})
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWebHeadlineIncludesInferredButNotManualUSD(t *testing.T) {
	var out strings.Builder
	data := webData{Report: core.Report{TotalVerified: 10, TotalInferred: 2.5, TotalManual: 3}}
	if err := webTemplate.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, want := range []string{
		`<div class="total">12.50 USD</div>`,
		`<small>Verified</small><strong>10.00 USD</strong>`,
		`<small>Inferred</small><strong>2.50 USD</strong>`,
		`<small>Manual</small><strong>3.00 USD</strong>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("web summary missing %q", want)
		}
	}
}

func TestWebInterfaceAndFormErrorsAreEnglish(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	h := newWebHandler(reg, filepath.Join(t.TempDir(), "web.yaml"), 20*time.Second, "test-web-token")
	w := webRequest(h, "GET", "http://127.0.0.1:8787/", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<html lang="en">`) ||
		!strings.Contains(w.Body.String(), "API-reported USD") ||
		!strings.Contains(w.Body.String(), "Add provider") ||
		strings.Contains(w.Body.String(), "Hozzáad") {
		t.Fatalf("dashboard is not in English: %d %s", w.Code, w.Body.String())
	}
	w = webRequest(h, "POST", "http://127.0.0.1:8787/add", "http://127.0.0.1:8787", url.Values{"id": {"groq"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "valid API key") {
		t.Fatalf("form error is not in English: %d %s", w.Code, w.Body.String())
	}
}

func TestWebNeuralwattLinksToCreditDashboard(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  neuralwatt: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEURALWATT_API_KEY", "") // No real network or credential is needed to render the link.
	w := webRequest(newWebHandler(reg, path, 20*time.Second, "test-web-token"), "GET", "http://127.0.0.1:8787/", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="https://portal.neuralwatt.com/dashboard"`) {
		t.Fatalf("Neuralwatt dashboard link missing: %d %s", w.Code, w.Body.String())
	}
}

func TestWebStoresKeyOnlyInPrivateFileAndNeverInConfig(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web.yaml")
	h := newWebHandler(reg, path, 20*time.Second, "test-web-token")
	key := "sk-test-web-secret-1234567890"
	form := url.Values{"id": {"groq"}, "key": {key}, "manual": {"25.00"}}
	w := webRequest(h, "POST", "http://127.0.0.1:8787/add", "http://127.0.0.1:8787", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), key) {
		t.Fatal("key reflected in response")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), key) {
		t.Fatal("config contains literal key")
	}
	cfg, err := core.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(filepath.Dir(path), "keys", "groq")
	if cfg.Providers["groq"].Key != "file:"+keyPath {
		t.Fatalf("wrong source: %q", cfg.Providers["groq"].Key)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("key mode = %o", info.Mode().Perm())
	}
	if b, err := os.ReadFile(keyPath); err != nil || string(b) != key {
		t.Error("key file does not contain the supplied key")
	}

	// A second add must not overwrite a saved credential.
	w = webRequest(h, "POST", "http://127.0.0.1:8787/add", "http://127.0.0.1:8787", url.Values{"id": {"groq"}, "key": {"different-key"}})
	if w.Code == http.StatusSeeOther {
		t.Fatal("duplicate add accepted")
	}
	b, _ = os.ReadFile(keyPath)
	if string(b) != key {
		t.Fatal("duplicate add replaced existing key")
	}
	w = webRequest(h, "POST", "http://127.0.0.1:8787/delete", "http://127.0.0.1:8787", url.Values{"id": {"groq"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("managed key file remains after deletion")
	}
}

func TestWebAcceptsSameOriginFormWithoutOriginHeader(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web.yaml")
	h := newWebHandler(reg, path, 20*time.Second, "test-web-token")
	form := url.Values{"id": {"groq"}, "key": {"sk-test-web-secret-1234567890"}}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8787/add", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(&http.Cookie{Name: "aipocket_web", Value: "test-web-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("same-origin form without Origin was rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestWebRejectsCrossOriginWritesAndUnrecognisedHosts(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web.yaml")
	h := newWebHandler(reg, path, 20*time.Second, "test-web-token")
	form := url.Values{"id": {"groq"}, "key": {"sk-test-web-secret-1234567890"}}
	w := webRequest(h, "POST", "http://127.0.0.1:8787/add", "https://evil.example", form)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin post: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8787/add", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: "aipocket_web", Value: "test-web-token"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site post without Origin: %d", w.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cross-origin request wrote config")
	}
	r = httptest.NewRequest("GET", "http://evil.example/", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unrecognised Host: %d", w.Code)
	}
}

func TestWebRequiresStartupTokenEvenFromLoopback(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	h := newWebHandler(reg, filepath.Join(t.TempDir(), "web.yaml"), 20*time.Second, "test-web-token")
	r := httptest.NewRequest("GET", "http://127.0.0.1:8787/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated localhost read: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:8787/?token=test-web-token", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].HttpOnly {
		t.Fatalf("login did not set an HTTP-only cookie: %d", w.Code)
	}
	if w.Result().Header.Get("Location") != "/" {
		t.Fatal("login left token in the page URL")
	}
	w = webRequest(h, "GET", "http://127.0.0.1:8787/", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated read: %d", w.Code)
	}
}

func TestWebAuditListsExactlyConfiguredActiveProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIPOCKET_CONFIG", filepath.Join(dir, "config.yaml"))
	webPath := filepath.Join(dir, "web.yaml")
	if err := os.WriteFile(webPath, []byte("providers:\n  groq:\n    key: env:GROQ_API_KEY\n  deepseek:\n    disabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out := runArgs(t, "audit", "web", "--json")
	if code != 0 || !strings.Contains(out, `"provider": "groq"`) || strings.Contains(out, `"provider": "deepseek"`) || strings.Contains(out, `"provider": "openrouter"`) {
		t.Fatalf("web audit is not exact: exit %d: %s", code, out)
	}
}

func TestWebDisabledProviderDoesNotHideOtherRows(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  groq:\n    disabled: true\n  deepseek: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	h := newWebHandler(reg, path, 20*time.Second, "test-web-token")
	w := webRequest(h, "GET", "http://127.0.0.1:8787/", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "DeepSeek") || !strings.Contains(w.Body.String(), "disabled") {
		t.Fatalf("disabled provider erased dashboard: %d %s", w.Code, w.Body.String())
	}
}

func TestWebDeleteKeepsUnmanagedCredentialFiles(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "web.yaml")
	external := filepath.Join(dir, "elsewhere", "secret")
	if err := os.MkdirAll(filepath.Dir(external), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("providers:\n  groq:\n    key: file:"+external+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := newWebHandler(reg, path, 20*time.Second, "test-web-token")
	w := webRequest(h, "POST", "http://127.0.0.1:8787/delete", "http://127.0.0.1:8787", url.Values{"id": {"groq"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("deleted unrelated credential: %v", err)
	}
}
