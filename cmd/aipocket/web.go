package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/money"
	"gopkg.in/yaml.v3"
)

// Hallmark · macrostructure: Stat-Led · tone: utilitarian · anchor hue: green
const webPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>AIPocket · Balances</title>
<style>
:root{--color-paper:#f3f5f0;--color-panel:#fff;--color-ink:#192520;--color-muted:#52635a;--color-line:#d5dfd4;--color-accent:#174d37;--color-error:#9b2c25;--font-display:system-ui,sans-serif;--font-body:system-ui,sans-serif;--space-sm:8px;--space-md:16px;--space-lg:24px;--space-xl:40px;color:var(--color-ink);background:var(--color-paper);font-family:var(--font-body)}
*{box-sizing:border-box}html,body{overflow-x:clip}body{margin:0}main{max-width:960px;margin:auto;padding:var(--space-xl) var(--space-lg) 80px}header{display:flex;align-items:center;justify-content:space-between;gap:var(--space-lg);margin-bottom:var(--space-xl)}h1{font:700 clamp(2.4rem,6vw,4rem)/1.05 var(--font-display);letter-spacing:-.055em;margin:0;overflow-wrap:anywhere}h2{font-size:1.15rem;margin:0 0 var(--space-md)}p{line-height:1.5}.muted,small{color:var(--color-muted)}.muted{margin:var(--space-sm) 0 0}small{display:block;line-height:1.4;margin-top:4px}.panel{background:var(--color-panel);border:1px solid var(--color-line);border-radius:12px;padding:var(--space-lg);margin:var(--space-lg) 0}.summary{border-top:4px solid var(--color-accent)}.total{font-size:clamp(2.8rem,8vw,5rem);font-weight:700;letter-spacing:-.06em;line-height:1.1;font-variant-numeric:tabular-nums;margin:var(--space-md) 0}.totals{display:flex;flex-wrap:wrap;gap:var(--space-lg);border-top:1px solid var(--color-line);padding-top:var(--space-md)}.totals strong{display:block;font-variant-numeric:tabular-nums}.row{display:flex;justify-content:space-between;align-items:center;gap:var(--space-md);padding:var(--space-md) 0;border-top:1px solid var(--color-line)}.row:first-of-type{border-top:0}.row>div:first-child{min-width:0}.name{font-weight:700}.amount{font-size:1.2rem;font-weight:700;font-variant-numeric:tabular-nums;white-space:nowrap}.actions{display:flex;align-items:center;gap:var(--space-md)}button{background:var(--color-accent);color:var(--color-panel);border:1px solid var(--color-accent);border-radius:6px;padding:10px 16px;cursor:pointer;font:600 1rem var(--font-body);white-space:nowrap}button:hover{filter:brightness(1.15)}button:active{transform:translateY(1px)}button:focus-visible,input:focus-visible,select:focus-visible,a:focus-visible{outline:3px solid var(--color-accent);outline-offset:3px}button.danger{background:transparent;color:var(--color-error);border:1px solid var(--color-error);padding:7px 10px}button.danger:hover{background:var(--color-error);color:var(--color-panel)}label{display:block;font-weight:600;margin:var(--space-md) 0}input,select{display:block;font:inherit;border:1px solid var(--color-muted);border-radius:6px;padding:10px;width:100%;margin-top:var(--space-sm);background:var(--color-panel);color:var(--color-ink)}.grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 var(--space-md)}a{color:var(--color-accent)}.note{margin-top:var(--space-md)}@media(max-width:620px){main{padding:var(--space-lg) var(--space-md) 60px}header{align-items:flex-start;flex-direction:column}.grid{grid-template-columns:minmax(0,1fr)}.row{align-items:flex-start;flex-direction:column}.actions{width:100%;justify-content:space-between}}@media(prefers-reduced-motion:reduce){button:active{transform:none}}
</style></head><body><main><header><div><h1>AIPocket</h1><p class="muted">Local balance dashboard</p></div><form method="get" action="/"><button type="submit">Refresh</button></form></header>
<section class="panel summary" aria-label="Balance summary"><div class="muted">API-reported USD (verified + inferred)</div><div class="total">{{printf "%.2f" .APIReportedUSD}} USD</div><div class="totals"><div><small>Verified</small><strong>{{printf "%.2f" .Report.TotalVerified}} USD</strong></div><div><small>Inferred</small><strong>{{printf "%.2f" .Report.TotalInferred}} USD</strong></div><div><small>Manual</small><strong>{{printf "%.2f" .Report.TotalManual}} USD</strong></div></div><p class="muted">The API total includes inferred fields such as Neuralwatt. Only documented fields count as verified; manual amounts and other currencies are excluded.</p></section>
<section class="panel"><h2>Added providers</h2>{{if not .Rows}}<p class="muted">No providers added yet.</p>{{end}}{{range .Rows}}<div class="row"><div><div class="name">{{.Name}}</div><small>{{.Status}}{{if .Detail}} · {{.Detail}}{{end}}</small>{{if .Console}}<small><a href="{{.Console}}" rel="noreferrer noopener" target="_blank">Provider dashboard</a></small>{{end}}</div><div class="actions"><div class="amount">{{.Amount}}</div><form method="post" action="/delete"><input type="hidden" name="id" value="{{.ID}}"><button class="danger" type="submit" aria-label="Remove {{.Name}}">Remove</button></form></div></div>{{end}}</section>
<section class="panel"><h2>Add provider</h2>{{if .Options}}<form method="post" action="/add"><div class="grid"><label>Provider<select name="id" required>{{range .Options}}<option value="{{.ID}}">{{.Name}}{{if .NoAPI}} · no balance API{{end}}</option>{{end}}</select></label><label>API key<input type="password" name="key" autocomplete="off" required maxlength="4096"></label></div><label>Manual USD balance (providers without a balance API only)<input type="number" step="0.01" min="0" name="manual" placeholder="Optional"></label><button type="submit">Add provider</button></form>{{else}}<p class="muted">All known providers have been added.</p>{{end}}<p class="muted note">Your key is stored only on this machine in a file readable only by you. Refresh to check balances again.</p></section>
</main></body></html>`

var webTemplate = template.Must(template.New("web").Parse(webPage))

type webRow struct{ ID, Name, Status, Detail, Amount, Console string }
type webOption struct {
	ID, Name string
	NoAPI    bool
}
type webData struct {
	Report  core.Report
	Rows    []webRow
	Options []webOption
}

func (d webData) APIReportedUSD() float64 { return d.Report.TotalVerified + d.Report.TotalInferred }

func serveWeb(ctx context.Context, reg *manifest.Registry, configPath string, timeout time.Duration) int {
	path, err := filepath.Abs(filepath.Join(filepath.Dir(configPath), "web.yaml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: web: %v\n", err)
		return 1
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: web: cannot create login token: %v\n", err)
		return 1
	}
	token := hex.EncodeToString(tokenBytes)
	handler := newWebHandler(reg, path, timeout, token)
	listener, err := net.Listen("tcp", "127.0.0.1:8787")
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: web: %v\n", err)
		return 1
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Printf("AIPocket: open http://127.0.0.1:8787/?token=%s\n", token)
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "aipocket: web: %v\n", err)
		return 1
	}
	return 0
}

func newWebHandler(reg *manifest.Registry, path string, timeout time.Duration, token string) http.Handler {
	var mu sync.Mutex // Serialize read-modify-write requests; separate browser tabs must not lose an update.
	mux := http.NewServeMux()
	load := func() (*core.Config, error) {
		cfg, err := core.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		return cfg, cfg.ValidateAgainst(reg)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cfg, err := load()
		mu.Unlock()
		if err != nil {
			http.Error(w, "Cannot read web configuration.", http.StatusInternalServerError)
			return
		}
		data := webData{}
		checker := core.NewChecker(reg, cfg, timeout)
		ids := make([]string, 0, len(cfg.Providers))
		for id, pc := range cfg.Providers {
			if !pc.Disabled {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			selected, err := checker.Selected(ids)
			if err != nil {
				http.Error(w, "Cannot load providers.", http.StatusInternalServerError)
				return
			}
			data.Report = checker.Run(r.Context(), selected)
		}
		for _, result := range data.Report.Results {
			amount := "—"
			if result.Balance != nil {
				amount = fmt.Sprintf("%.2f %s", *result.Balance, result.Currency)
			}
			status := string(result.State)
			if result.State == core.StateOK {
				status = string(result.Confidence)
			}
			detail := result.Detail
			if result.Error != "" {
				detail = result.Error
			}
			data.Rows = append(data.Rows, webRow{result.ID, result.Name, status, detail, amount, result.Console})
		}
		for _, p := range reg.All() {
			if pc, ok := cfg.Providers[p.ID]; ok {
				if pc.Disabled {
					data.Rows = append(data.Rows, webRow{ID: p.ID, Name: p.Name, Status: "disabled", Amount: "—", Console: p.Console})
				}
			} else {
				data.Options = append(data.Options, webOption{p.ID, p.Name, p.Balance == nil})
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := webTemplate.Execute(w, data); err != nil {
			fmt.Fprintf(os.Stderr, "aipocket: web: template: %v\n", err)
		}
	})
	mux.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form.", http.StatusBadRequest)
			return
		}
		id, key := r.PostForm.Get("id"), strings.TrimSpace(r.PostForm.Get("key"))
		p, ok := reg.Get(id)
		if !ok || len(r.PostForm["id"]) != 1 || len(r.PostForm["key"]) != 1 || len(key) == 0 || len(key) > 4096 || !printableKey(key) {
			http.Error(w, "Select a provider and enter a valid API key.", http.StatusBadRequest)
			return
		}
		pc := core.ProviderConfig{}
		if len(r.PostForm["manual"]) > 1 {
			http.Error(w, "Invalid manual balance.", http.StatusBadRequest)
			return
		}
		if s := r.PostForm.Get("manual"); s != "" {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || v < 0 || !money.Plausible(v) || p.Balance != nil {
				http.Error(w, "Manual balances are only available for providers without a balance API.", http.StatusBadRequest)
				return
			}
			pc.Manual = &v
			pc.AsOf = time.Now().UTC().Format("2006-01-02")
		}
		mu.Lock()
		defer mu.Unlock()
		cfg, err := load()
		if err != nil {
			http.Error(w, "Cannot read web configuration.", http.StatusInternalServerError)
			return
		}
		if _, exists := cfg.Providers[id]; exists {
			http.Error(w, "This provider has already been added.", http.StatusConflict)
			return
		}
		keyDir := filepath.Join(filepath.Dir(path), "keys")
		if err := os.MkdirAll(keyDir, 0700); err != nil {
			http.Error(w, "Cannot save API key.", http.StatusInternalServerError)
			return
		}
		keyPath := filepath.Join(keyDir, id)
		f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			http.Error(w, "Cannot save API key.", http.StatusInternalServerError)
			return
		}
		_, writeErr := f.WriteString(key)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(keyPath)
			http.Error(w, "Cannot save API key.", http.StatusInternalServerError)
			return
		}
		pc.Key = "file:" + keyPath
		cfg.Providers[id] = pc
		if err := saveWebConfig(path, cfg); err != nil {
			_ = os.Remove(keyPath)
			http.Error(w, "Cannot save settings.", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /delete", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil || len(r.PostForm["id"]) != 1 {
			http.Error(w, "Invalid form.", http.StatusBadRequest)
			return
		}
		id := r.PostForm.Get("id")
		if _, ok := reg.Get(id); !ok {
			http.Error(w, "Unknown provider.", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		cfg, err := load()
		if err != nil {
			http.Error(w, "Cannot read web configuration.", http.StatusInternalServerError)
			return
		}
		pc, exists := cfg.Providers[id]
		if !exists {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		delete(cfg.Providers, id)
		if err := saveWebConfig(path, cfg); err != nil {
			http.Error(w, "Cannot save settings.", http.StatusInternalServerError)
			return
		}
		keyPath := filepath.Join(filepath.Dir(path), "keys", id)
		if pc.Key == "file:"+keyPath {
			if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
				http.Error(w, "Cannot delete key file; remove it manually.", http.StatusInternalServerError)
				return
			}
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:8787" && r.Host != "localhost:8787" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			origin, site := r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site")
			// Some browsers omit Origin on same-origin form posts. The session
			// cookie is SameSite=Strict; still refuse explicit cross-site signals.
			if (site != "" && site != "same-origin") ||
				(origin != "" && origin != "http://"+r.Host && !(origin == "null" && site == "same-origin")) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet && r.URL.Path == "/" && r.URL.Query().Has("token") {
			if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "aipocket_web", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		cookie, err := r.Cookie("aipocket_web")
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func printableKey(s string) bool {
	for _, c := range s {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

func saveWebConfig(path string, cfg *core.Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".web-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
