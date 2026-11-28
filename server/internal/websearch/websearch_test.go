package websearch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agent-console/server/internal/webfetch"
)

func TestSearXNGSearchAndSSRFGuarantee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"results":[{"title":"Go 1.26","url":"https://go.dev/blog","content":"Go release notes"},{"title":"Second","url":"https://example.org","content":"Example snippet"}]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	results, err := c.Search(t.Context(), "go 1.26", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Title != "Go 1.26" || results[0].URL != "https://go.dev/blog" {
		t.Fatalf("unexpected search results: %+v", results)
	}

	ctxBlock := BuildSearchContext("Что нового?", "go 1.26", results, nil)
	if !strings.Contains(ctxBlock, `<web_search_results query="go 1.26">`) ||
		!strings.Contains(ctxBlock, "UNTRUSTED EXTERNAL WEB CONTENT") ||
		!strings.Contains(ctxBlock, "https://go.dev/blog") {
		t.Fatalf("unexpected search context block: %s", ctxBlock)
	}

	// Ensure user-supplied internal SearXNG URL is STILL blocked by webfetch SSRF protection.
	internalURL, _ := url.Parse("http://searxng:8080/search?q=test&format=json")
	if err := webfetch.ValidateTargetURL(internalURL); err == nil {
		t.Fatalf("expected webfetch SSRF validator to block http://searxng:8080")
	}
}
