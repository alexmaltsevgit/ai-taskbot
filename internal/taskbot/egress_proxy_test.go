package taskbot

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func proxyTestConfig(t *testing.T, endpoint string) ProxyConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxy.json")
	raw, _ := json.Marshal(map[string]string{"url": endpoint, "username": "alice", "password": "private-value"})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return config
}
func proxyTestServer(t *testing.T, endpoint string) (*EgressProxy, *httptest.Server) {
	t.Helper()
	proxy, err := NewEgressProxy(proxyTestConfig(t, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(func() { _ = proxy.Close(); server.Close() })
	return proxy, server
}
func TestProxyConfigurationValidationAndRedaction(t *testing.T) {
	good := proxyTestConfig(t, "http://127.0.0.1:3128")
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, good), "private-value") {
			t.Fatal("configuration formatting exposes credentials")
		}
	}
	for _, endpoint := range []string{"http://proxy.example:3128", "https://127.0.0.1:3128", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://alice:private-value@127.0.0.1:3128", "http://127.0.0.1:3128/", "http://127.0.0.1:3128?", "http://127.0.0.1:3128#secret", "http://127.0.0.1:3128#"} {
		t.Run(endpoint, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "proxy.json")
			raw, _ := json.Marshal(map[string]string{"url": endpoint, "username": "alice", "password": "private-value"})
			_ = os.WriteFile(path, raw, 0600)
			_, err := LoadProxyConfig(path)
			if err == nil || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), endpoint) {
				t.Fatal("validation or error redaction failed")
			}
		})
	}
	for _, raw := range []string{`{"url":"http://127.0.0.1:3128","username":"a:b","password":"p"}`, `{"url":"http://127.0.0.1:3128","username":"a","password":""}`, `{"url":"http://127.0.0.1:3128","username":"a","password":"p","extra":1}`, `{} {}`, strings.Repeat("x", 16385)} {
		path := filepath.Join(t.TempDir(), "proxy.json")
		_ = os.WriteFile(path, []byte(raw), 0600)
		if _, err := LoadProxyConfig(path); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if _, err := NewEgressProxy(ProxyConfig{}); err == nil {
		t.Fatal("zero configuration accepted")
	}
}
func TestProxyHTTPUsesOnlyUpstreamAndReplacesAuthentication(t *testing.T) {
	observed := make(chan *http.Request, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Clone(r.Context())
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Errorf("body=%q", body)
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 13\r\nConnection: X-Hop\r\nX-Hop: remove\r\nProxy-Authenticate: private-value\r\n\r\nfrom upstream")
		buffer.Flush()
	}))
	defer upstream.Close()
	_, proxy := proxyTestServer(t, upstream.URL)
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(conn, "POST http://destination.invalid/path?q=1 HTTP/1.1\r\nHost: spoofed.invalid\r\nProxy-Authorization: Basic injected\r\nConnection: X-Injected\r\nX-Injected: strip\r\nContent-Length: 7\r\n\r\npayload")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(body) != "from upstream" || response.Header.Get("X-Hop") != "" || response.Header.Get("Proxy-Authenticate") != "" {
		t.Fatal(response.StatusCode, string(body), response.Header)
	}
	got := <-observed
	if got.RequestURI != "http://destination.invalid/path?q=1" || got.Host != "destination.invalid" || got.Header.Get("X-Injected") != "" || got.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:private-value")) {
		t.Fatal("incorrect upstream request")
	}
}
func TestProxyCONNECTBufferedBytesAndBidirectionalTunnel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			result <- err
			return
		}
		if request.Method != "CONNECT" || request.RequestURI != "destination.invalid:443" || request.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:private-value")) {
			result <- fmt.Errorf("incorrect CONNECT")
			return
		}
		// Greeting arrives in the same read as response headers: it must not be lost.
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\ngreeting")
		bytes := make([]byte, 5)
		if _, err = io.ReadFull(reader, bytes); err != nil || string(bytes) != "early" {
			result <- fmt.Errorf("buffered client bytes missing: %v", err)
			return
		}
		fmt.Fprint(conn, "reply")
		bytes = make([]byte, 4)
		if _, err = io.ReadFull(reader, bytes); err != nil || string(bytes) != "late" {
			result <- fmt.Errorf("client data missing: %v", err)
			return
		}
		result <- nil
	}()
	_, proxy := proxyTestServer(t, "http://"+listener.Addr().String())
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	// Payload is deliberately buffered with the client's CONNECT request.
	fmt.Fprint(conn, "CONNECT destination.invalid:443 HTTP/1.1\r\nHost: destination.invalid:443\r\nProxy-Authorization: injected\r\n\r\nearly")
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response, err)
	}
	bytes := make([]byte, len("greetingreply"))
	if _, err = io.ReadFull(reader, bytes); err != nil || string(bytes) != "greetingreply" {
		t.Fatal(string(bytes), err)
	}
	fmt.Fprint(conn, "late")
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
func TestProxyFailureNeverDialsDestination(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("direct target request escaped") }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-value", 407) }))
	defer upstream.Close()
	_, proxy := proxyTestServer(t, upstream.URL)
	for _, requestLine := range []string{"GET " + target.URL + "/ HTTP/1.1", "CONNECT " + strings.TrimPrefix(target.URL, "http://") + " HTTP/1.1"} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(conn, "%s\r\nHost: %s\r\n\r\n", requestLine, strings.TrimPrefix(target.URL, "http://"))
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		conn.Close()
		if response.StatusCode != 502 || strings.Contains(string(body), "private-value") {
			t.Fatal(response.StatusCode, string(body))
		}
	}
}
func TestProxyCloseTerminatesActiveTunnel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer.WriteString("HTTP/1.1 200 OK\r\n\r\n")
		buffer.Flush()
		io.Copy(io.Discard, buffer)
	}))
	defer upstream.Close()
	proxy, server := proxyTestServer(t, upstream.URL)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(conn, "CONNECT destination.invalid:443 HTTP/1.1\r\nHost: destination.invalid:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	if _, err = http.ReadResponse(reader, &http.Request{Method: http.MethodConnect}); err != nil {
		t.Fatal(err)
	}
	_ = proxy.Close()
	if _, err = reader.ReadByte(); err == nil {
		t.Fatal("tunnel survived Close")
	}
}

func TestProxyRejectsUnsupportedTargetsWithoutDialing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request forwarded") }))
	defer upstream.Close()
	proxy, _ := proxyTestServer(t, upstream.URL)
	for _, target := range []string{"/healthz", "https://destination.invalid/", "ftp://destination.invalid/", "http://name:pass@destination.invalid/"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatal(target, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodConnect, "http://localhost/", nil)
	request.Host = "invalid:0"
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatal(response.Code)
	}
}
func TestProxyBoundsUpstreamHeadersAndDoesNotFollowRedirects(t *testing.T) {
	for _, scenario := range []string{"oversized", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if scenario == "redirect" {
					w.Header().Set("Location", "http://redirect.invalid/")
					w.WriteHeader(302)
					return
				}
				conn, buffer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				buffer.WriteString("HTTP/1.1 200 OK\r\nX-Large: " + strings.Repeat("x", proxyHeaderLimit) + "\r\n\r\n")
				buffer.Flush()
			}))
			defer upstream.Close()
			proxy, _ := proxyTestServer(t, upstream.URL)
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://destination.invalid/", nil))
			expected := 502
			if scenario == "redirect" {
				expected = 302
			}
			if response.Code != expected || requests != 1 {
				t.Fatal(response.Code, requests)
			}
		})
	}
}
func TestProxyUnavailableUpstreamDoesNotFallBack(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("direct fallback occurred") }))
	defer target.Close()
	proxy, _ := proxyTestServer(t, "http://"+address)
	result := httptest.NewRecorder()
	proxy.ServeHTTP(result, httptest.NewRequest(http.MethodGet, target.URL, nil))
	if result.Code != 502 {
		t.Fatal(result.Code)
	}
}

func TestProxyCredentialFileMustBePrivateRegularFile(t *testing.T) {
	raw := []byte(`{"url":"http://127.0.0.1:3128","username":"alice","password":"private-value"}`)
	for _, scenario := range []string{"group-readable", "world-readable", "not-owner-readable", "directory", "symlink", "fifo"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "credentials.json")
			switch scenario {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target.json")
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0640)
				if scenario == "world-readable" {
					mode = 0604
				}
				if scenario == "not-owner-readable" {
					mode = 0200
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			go func() { _, err := LoadProxyConfig(path); result <- err }()
			select {
			case err := <-result:
				if err == nil || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), path) {
					t.Fatal("unsafe file accepted or error leaked file details")
				}
			case <-time.After(time.Second):
				t.Fatal("credential loading blocked on nonregular file")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "readonly.json")
	if err := os.WriteFile(path, raw, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProxyConfig(path); err != nil {
		t.Fatal("private read-only file rejected", err)
	}
}

func TestProxyConcurrencyLimitAndRecovery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer.WriteString("HTTP/1.1 200 OK\r\n\r\n")
		buffer.Flush()
		io.Copy(io.Discard, buffer)
	}))
	defer upstream.Close()
	proxy, server := proxyTestServer(t, upstream.URL)
	connections := make([]net.Conn, proxyConcurrencyLimit)
	defer func() {
		for _, conn := range connections {
			if conn != nil {
				conn.Close()
			}
		}
	}()
	openTunnel := func() (net.Conn, error) {
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprint(conn, "CONNECT destination.invalid:443 HTTP/1.1\r\nHost: destination.invalid:443\r\n\r\n")
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		if err != nil {
			conn.Close()
			return nil, err
		}
		if response.StatusCode != 200 {
			conn.Close()
			return nil, fmt.Errorf("unexpected tunnel status %d", response.StatusCode)
		}
		return conn, nil
	}
	// Concurrent callers must consume exactly one slot each for the entire tunnel.
	var group sync.WaitGroup
	errors := make(chan error, proxyConcurrencyLimit)
	for i := range connections {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			conn, err := openTunnel()
			if err != nil {
				errors <- err
				return
			}
			connections[i] = conn
		}(i)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if got := len(proxy.slots); got != proxyConcurrencyLimit {
		t.Fatal("wrong active count", got)
	}
	rejected := httptest.NewRecorder()
	proxy.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "http://destination.invalid/", nil))
	if rejected.Code != 503 {
		t.Fatal("exhaustion did not reject", rejected.Code)
	}
	connections[0].Close()
	connections[0] = nil
	deadline := time.Now().Add(3 * time.Second)
	for len(proxy.slots) == proxyConcurrencyLimit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(proxy.slots) != proxyConcurrencyLimit-1 {
		t.Fatal("closed tunnel did not release slot")
	}
	replacement, err := openTunnel()
	if err != nil {
		t.Fatal("capacity failed to recover", err)
	}
	connections[0] = replacement
	_ = proxy.Close()
	deadline = time.Now().Add(3 * time.Second)
	for len(proxy.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(proxy.slots) != 0 {
		t.Fatal("shutdown leaked slots", len(proxy.slots))
	}
	afterClose := httptest.NewRecorder()
	proxy.ServeHTTP(afterClose, httptest.NewRequest(http.MethodGet, "http://destination.invalid/", nil))
	if afterClose.Code != 503 || len(proxy.slots) != 0 {
		t.Fatal("closed proxy admitted work or leaked slot")
	}
}
func TestProxyReleasesSlotsOnErrorPaths(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	proxy, _ := proxyTestServer(t, "http://"+address)
	for _, target := range []string{"/invalid", "http://destination.invalid/"} {
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code < 400 || len(proxy.slots) != 0 {
			t.Fatal("error leaked slot", response.Code, len(proxy.slots))
		}
	}
}

func TestProxyInformationalResponses(t *testing.T) {
	for _, scenario := range []string{"continue", "early-hints", "too-many", "upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Expect") != "" {
					t.Error("Expect forwarded without handshake")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "payload" {
					t.Errorf("body=%q err=%v", body, err)
				}
				conn, buffer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				switch scenario {
				case "continue":
					buffer.WriteString("HTTP/1.1 100 Continue\r\n\r\n")
				case "early-hints":
					buffer.WriteString("HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\nHTTP/1.1 100 Continue\r\n\r\n")
				case "too-many":
					for i := 0; i <= proxyInterimLimit; i++ {
						buffer.WriteString("HTTP/1.1 100 Continue\r\n\r\n")
					}
				case "upgrade":
					buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
				}
				buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nfinal")
				buffer.Flush()
			}))
			defer upstream.Close()
			proxy, _ := proxyTestServer(t, upstream.URL)
			request := httptest.NewRequest(http.MethodPost, "http://destination.invalid/", strings.NewReader("payload"))
			request.Header.Set("Expect", "100-continue")
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, request)
			if scenario == "too-many" || scenario == "upgrade" {
				if response.Code != 502 {
					t.Fatal(response.Code)
				}
			} else if response.Code != 200 || response.Body.String() != "final" || response.Header().Get("Link") != "" {
				t.Fatal(response.Code, response.Body.String())
			}
			if len(proxy.slots) != 0 {
				t.Fatal("slot leaked")
			}
		})
	}
}
