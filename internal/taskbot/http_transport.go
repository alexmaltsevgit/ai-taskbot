package taskbot

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

const LocalEgressProxy = "http://egress:3128"
const InternalNoProxy = "worker,localhost,127.0.0.1,::1"

func configuredEgressProxy() (*url.URL, error) {
	address := os.Getenv("EGRESS_PROXY_URL")
	if os.Getenv("STRICT_EGRESS") == "true" && address != LocalEgressProxy {
		return nil, fmt.Errorf("strict egress requires the local proxy")
	}
	if address == "" {
		return nil, nil
	}
	proxy, err := url.Parse(address)
	if err != nil || proxy.Scheme != "http" || proxy.Hostname() == "" || proxy.Port() == "" || proxy.User != nil || proxy.RawQuery != "" || proxy.Fragment != "" || (proxy.Path != "" && proxy.Path != "/") {
		return nil, fmt.Errorf("invalid local egress proxy configuration")
	}
	return proxy, nil
}

// External requests never consult NO_PROXY when an explicit egress is configured.
// Production therefore cannot accidentally bypass the sidecar with NO_PROXY=*.
func NewExternalHTTPClient(timeout time.Duration) (*http.Client, error) {
	proxy, err := configuredEgressProxy()
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := NewHTTPClient(timeout)
	client.Transport = transport
	return client, nil
}

// The worker is an internal service, not an external URL to tunnel via egress.
func NewDirectHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := NewHTTPClient(timeout)
	client.Transport = transport
	return client
}

func CodexEnvironment() []string {
	environment := []string{}
	for _, key := range []string{"PATH", "HOME", "CODEX_HOME", "LANG", "SSL_CERT_FILE"} {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	if os.Getenv("STRICT_EGRESS") == "true" {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			environment = append(environment, key+"="+LocalEgressProxy)
		}
		environment = append(environment, "NO_PROXY="+InternalNoProxy, "no_proxy="+InternalNoProxy)
	} else {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
			if value, ok := os.LookupEnv(key); ok {
				environment = append(environment, key+"="+value)
			}
		}
	}
	return environment
}
