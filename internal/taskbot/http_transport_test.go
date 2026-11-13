package taskbot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExplicitEgressDoesNotHonorWildcardNoProxy(t *testing.T) {
	requests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Host != "external.invalid" {
			t.Error(r.URL)
		}
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	t.Setenv("EGRESS_PROXY_URL", proxy.URL)
	t.Setenv("STRICT_EGRESS", "")
	t.Setenv("NO_PROXY", "*")
	client, err := NewExternalHTTPClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://external.invalid/resource")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if requests != 1 {
		t.Fatal("proxy bypassed")
	}
}
func TestInternalWorkerTransportIsDirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	t.Setenv("HTTP_PROXY", "http://unreachable.invalid:3128")
	t.Setenv("NO_PROXY", "")
	client := NewDirectHTTPClient(time.Second)
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("internal client uses proxy")
	}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}
func TestStrictEgressRequiresSidecarAndSanitizesCodex(t *testing.T) {
	t.Setenv("STRICT_EGRESS", "true")
	t.Setenv("EGRESS_PROXY_URL", "")
	if _, err := NewExternalHTTPClient(time.Second); err == nil {
		t.Fatal("missing strict proxy accepted")
	}
	t.Setenv("EGRESS_PROXY_URL", LocalEgressProxy)
	client, err := NewExternalHTTPClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	actual, err := client.Transport.(*http.Transport).Proxy(request)
	if err != nil || actual.String() != LocalEgressProxy {
		t.Fatal(actual, err)
	}
	t.Setenv("GITHUB_TOKEN", "secret-github")
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret-telegram")
	t.Setenv("WORKER_TOKEN", "secret-worker")
	t.Setenv("NO_PROXY", "*")
	t.Setenv("HTTPS_PROXY", "http://user:password@other:80")
	environment := strings.Join(CodexEnvironment(), "\n")
	for _, forbidden := range []string{"secret-github", "secret-telegram", "secret-worker", "password@", "NO_PROXY=*"} {
		if strings.Contains(environment, forbidden) {
			t.Fatal("unsafe child environment")
		}
	}
	for _, expected := range []string{"HTTP_PROXY=" + LocalEgressProxy, "HTTPS_PROXY=" + LocalEgressProxy, "ALL_PROXY=" + LocalEgressProxy, "NO_PROXY=" + InternalNoProxy} {
		if !strings.Contains(environment, expected) {
			t.Fatal("missing proxy key", expected)
		}
	}
}
