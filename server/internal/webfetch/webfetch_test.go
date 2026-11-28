package webfetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestExtractURLsDeduplicatesAndLimits(t *testing.T) {
	text := "Посмотри https://example.com/a, https://example.com/a. Потом http://example.org/b?x=1! И https://example.net/c_(1) и https://fourth.example/d"
	urls := ExtractURLs(text, MaxURLsPerMessage)
	want := []string{
		"https://example.com/a",
		"http://example.org/b?x=1",
		"https://example.net/c_(1)",
	}
	if len(urls) != len(want) {
		t.Fatalf("got %d urls %+v, want %+v", len(urls), urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("url[%d] = %q, want %q", i, urls[i], want[i])
		}
	}
}

func TestSSRFBlocksInternalAndSpecialAddresses(t *testing.T) {
	f := NewFetcher()
	blockedURLs := []string{
		"http://127.0.0.1",
		"http://127.0.0.1:8080/api/health",
		"http://localhost/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/",
		"http://172.17.0.1/",
		"http://192.168.1.1/",
		"http://100.64.0.1/", // CGNAT / Tailscale
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[fc00::1]/",
		"http://core:8080/api/health",
		"http://unlocker:8080/",
		"http://example.com:8443/",
		"https://example.com:80/",
		"http://example.com:443/",
	}
	for _, target := range blockedURLs {
		p := f.Fetch(t.Context(), target)
		if p.Status != StatusBlocked {
			t.Fatalf("expected StatusBlocked for %q, got %+v", target, p)
		}
	}

	// Verify Dialer Control directly blocks loopback/private/CGNAT/bad-port even after DNS resolution
	for _, dialAddr := range []string{
		"127.0.0.1:80",
		"169.254.169.254:80",
		"10.0.0.1:443",
		"172.17.0.1:80",
		"100.64.0.1:443",
		"[::1]:80",
		"[::ffff:127.0.0.1]:80",
		"93.184.216.34:8080",
	} {
		if err := ValidateDialAddress(dialAddr); err == nil {
			t.Fatalf("ValidateDialAddress(%q) should have failed", dialAddr)
		}
	}
	if err := ValidateDialAddress("93.184.216.34:443"); err != nil {
		t.Fatalf("ValidateDialAddress public 443 failed: %v", err)
	}
}

func TestSSRFBlocksRedirectToLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/secret", http.StatusFound)
	}))
	defer srv.Close()

	srvURL, _ := url.Parse(srv.URL)

	f := NewFetcher()
	// Route external.example:80 to our mock redirector, while CheckRedirect & Control remain active.
	f.client.Transport = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, _ := net.SplitHostPort(addr)
			if host == "external.example" && port == "80" {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, srvURL.Host)
			}
			// Any other address (including 127.0.0.1 redirect!) goes through real ValidateDialAddress
			if err := ValidateDialAddress(addr); err != nil {
				return nil, err
			}
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
		},
	}

	p := f.Fetch(t.Context(), "http://external.example/start")
	if p.Status != StatusBlocked {
		t.Fatalf("expected redirect to 127.0.0.1 to be blocked, got %+v", p)
	}
}

func newTestFetcherForServer(srv *httptest.Server) *Fetcher {
	srvURL, _ := url.Parse(srv.URL)
	f := NewFetcher()
	f.client.Transport = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if host == "mock.example" {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, srvURL.Host)
			}
			return nil, ErrBlockedHost
		},
	}
	return f
}

func TestFetchHTMLExtractionSPAAndSizeLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Заголовок статьи</title><script>alert(1)</script><style>body{}</style></head>
		<body><nav>Меню сайта</nav><main><h1>Привет, мир</h1><p>Основной полезный текст страницы для анализа моделью и пользователем.</p></main><footer>Подвал</footer></body></html>`))
	})
	mux.HandleFunc("/spa", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>React App</title></head><body><div id="root"></div><script src="/bundle.js"></script></body></html>`))
	})
	mux.HandleFunc("/huge", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strings.Repeat("A", MaxBodyBytes+100)))
	})
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := newTestFetcherForServer(srv)

	art := f.Fetch(t.Context(), "http://mock.example/article")
	if art.Status != StatusOK || art.Title != "Заголовок статьи" {
		t.Fatalf("unexpected article result: %+v", art)
	}
	if !strings.Contains(art.Content, "Основной полезный текст") || strings.Contains(art.Content, "Меню сайта") || strings.Contains(art.Content, "alert(1)") {
		t.Fatalf("unexpected cleaned HTML content: %q", art.Content)
	}

	spa := f.Fetch(t.Context(), "http://mock.example/spa")
	if spa.Status != StatusJSRequired || spa.Detail != "js_required_or_empty" {
		t.Fatalf("expected StatusJSRequired for empty SPA, got %+v", spa)
	}

	huge := f.Fetch(t.Context(), "http://mock.example/huge")
	if huge.Status != StatusError || huge.Detail != "body_too_large" {
		t.Fatalf("expected body_too_large error, got %+v", huge)
	}

	forb := f.Fetch(t.Context(), "http://mock.example/forbidden")
	if forb.Status != StatusBlocked || forb.Detail != "http_403" {
		t.Fatalf("expected http_403 blocked, got %+v", forb)
	}

	prompt := BuildPromptContext("Проверь ссылку", []Page{art})
	if !strings.Contains(prompt, "UNTRUSTED EXTERNAL WEB CONTENT") || !strings.Contains(prompt, `<attached_webpage url="http://mock.example/article"`) {
		t.Fatalf("unexpected prompt wrapper: %q", prompt)
	}
}
