package taskbot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	proxyConcurrencyLimit = 128
	proxyInterimLimit     = 5
	proxyHeaderLimit      = 64 << 10
	proxyRequestBodyLimit = 16 << 20
	proxyRequestTimeout   = 5 * time.Minute
	proxyTunnelLifetime   = 30 * time.Minute
)

// ProxyConfig deliberately keeps credentials private and redacts formatting.
// The upstream is a literal IP, so this component never needs a DNS resolver.
type ProxyConfig struct {
	address       string
	authorization string
}

func (ProxyConfig) String() string   { return "ProxyConfig{<redacted>}" }
func (ProxyConfig) GoString() string { return "ProxyConfig{<redacted>}" }

func LoadProxyConfig(path string) (ProxyConfig, error) {
	// Check before opening so ordinary directories, symlinks and FIFOs are never
	// accepted. O_NOFOLLOW/O_NONBLOCK also close the replacement race between
	// Lstat and Open without waiting on a FIFO introduced by another process.
	initial, err := os.Lstat(path)
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("proxy credential file unavailable")
	}
	if !safeProxyCredentialFile(initial) {
		return ProxyConfig{}, fmt.Errorf("proxy credentials require an owner-readable regular file with no group or other access")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("proxy credential file unavailable")
	}
	file := os.NewFile(uintptr(fd), "proxy-credentials")
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !safeProxyCredentialFile(opened) || !os.SameFile(initial, opened) {
		return ProxyConfig{}, fmt.Errorf("proxy credential file changed or has unsafe permissions")
	}

	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 {
		return ProxyConfig{}, fmt.Errorf("invalid proxy credential file")
	}
	var config struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return ProxyConfig{}, fmt.Errorf("invalid proxy credential JSON")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return ProxyConfig{}, fmt.Errorf("invalid proxy credential JSON")
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || strings.Contains(config.URL, "#") || endpoint.Fragment != "" || endpoint.RawFragment != "" || endpoint.Opaque != "" {
		return ProxyConfig{}, fmt.Errorf("proxy URL must be http://literal-IP:port without credentials or other components")
	}
	ip := net.ParseIP(endpoint.Hostname())
	port, err := strconv.Atoi(endpoint.Port())
	if ip == nil || err != nil || port < 1 || port > 65535 {
		return ProxyConfig{}, fmt.Errorf("proxy URL requires a literal IP and explicit valid port")
	}
	if config.Username == "" || config.Password == "" || strings.ContainsAny(config.Username, ":\r\n") || strings.ContainsAny(config.Password, "\r\n") {
		return ProxyConfig{}, fmt.Errorf("invalid proxy credentials")
	}
	return ProxyConfig{address: net.JoinHostPort(ip.String(), strconv.Itoa(port)), authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte(config.Username+":"+config.Password))}, nil
}

func safeProxyCredentialFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Mode().Perm()&0400 != 0 && info.Size() <= 16384
}

// EgressProxy is an HTTP forward proxy which can dial only its configured
// upstream. No transport-level proxy discovery, target dialing or retry exists.
// Use Close alongside http.Server.Shutdown: Shutdown alone ignores hijacked
// CONNECT tunnels. Do not publish this unauthenticated listener on the host.
type EgressProxy struct {
	config      ProxyConfig
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	slots       chan struct{}
	closed      bool
}

func NewEgressProxy(config ProxyConfig) (*EgressProxy, error) {
	host, port, err := net.SplitHostPort(config.address)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || net.ParseIP(host) == nil || n < 1 || n > 65535 || config.authorization == "" {
		return nil, fmt.Errorf("invalid proxy configuration")
	}
	return &EgressProxy{config: config, connections: map[net.Conn]struct{}{}, slots: make(chan struct{}, proxyConcurrencyLimit)}, nil
}
func (p *EgressProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for conn := range p.connections {
		_ = conn.Close()
	}
	return nil
}
func (p *EgressProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = conn.Close()
		return false
	}
	p.connections[conn] = struct{}{}
	return true
}
func (p *EgressProxy) release(conn net.Conn) {
	_ = conn.Close()
	p.mu.Lock()
	delete(p.connections, conn)
	p.mu.Unlock()
}
func (p *EgressProxy) dial(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", p.config.address)
	if err != nil {
		return nil, fmt.Errorf("upstream proxy unavailable")
	}
	if !p.track(conn) {
		return nil, fmt.Errorf("proxy is shutting down")
	}
	return conn, nil
}
func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "Proxy capacity exhausted", http.StatusServiceUnavailable)
		return
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		http.Error(w, "Proxy is shutting down", http.StatusServiceUnavailable)
		return
	}

	if !boundedProxyHeaders(r.Header) {
		http.Error(w, "Request headers too large", http.StatusRequestHeaderFieldsTooLarge)
		return
	}
	if r.Method == http.MethodConnect {
		if !validConnectTarget(r.Host) || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			http.Error(w, "Invalid CONNECT request", http.StatusBadRequest)
			return
		}
		p.connect(w, r)
		return
	}
	if r.URL == nil || r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil || r.URL.Fragment != "" || r.URL.Opaque != "" || !validHTTPHost(r.URL.Host) || r.Header.Get("Upgrade") != "" {
		http.Error(w, "Only absolute-form HTTP and CONNECT are supported", http.StatusBadRequest)
		return
	}
	p.forwardHTTP(w, r)
}
func validConnectTarget(authority string) bool {
	host, port, err := net.SplitHostPort(authority)
	n, portErr := strconv.Atoi(port)
	return err == nil && portErr == nil && n > 0 && n <= 65535 && validProxyHost(host)
}
func validHTTPHost(authority string) bool {
	parsed, err := url.Parse("http://" + authority)
	if err != nil || parsed.Host != authority || parsed.User != nil || !validProxyHost(parsed.Hostname()) {
		return false
	}
	if parsed.Port() == "" {
		return true
	}
	n, err := strconv.Atoi(parsed.Port())
	return err == nil && n > 0 && n <= 65535
}
func validProxyHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func boundedProxyHeaders(headers http.Header) bool {
	size := 0
	for name, values := range headers {
		for _, value := range values {
			size += len(name) + len(value) + 4
			if size > proxyHeaderLimit {
				return false
			}
		}
	}
	return true
}
func removeProxyHopHeaders(headers http.Header) {
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(name)
	}
}

// ReadSlice bounds individual lines and the accumulated upstream response header;
// the original reader retains bytes received immediately after the final CRLF.
func readProxyResponseHeader(reader *bufio.Reader) ([]byte, error) {
	var header []byte
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil || len(header)+len(line) > proxyHeaderLimit {
			return nil, fmt.Errorf("invalid upstream response headers")
		}
		header = append(header, line...)
		if bytes.Equal(line, []byte("\r\n")) {
			return header, nil
		}
	}
}

// Parse interim headers separately so the parser cannot prefetch and discard
// bytes belonging to the final response. Bound both each header and the count.
func readFinalProxyResponse(reader *bufio.Reader, request *http.Request) (*http.Response, error) {
	for interim := 0; interim <= proxyInterimLimit; interim++ {
		header, err := readProxyResponseHeader(reader)
		if err != nil {
			return nil, err
		}
		response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), request)
		if err != nil {
			return nil, fmt.Errorf("invalid upstream response")
		}
		_ = response.Body.Close()
		if response.StatusCode == http.StatusSwitchingProtocols {
			return nil, fmt.Errorf("upstream protocol upgrade unsupported")
		}
		if response.StatusCode >= 200 {
			return http.ReadResponse(bufio.NewReader(io.MultiReader(bytes.NewReader(header), reader)), request)
		}
	}
	return nil, fmt.Errorf("too many upstream informational responses")
}

func (p *EgressProxy) forwardHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := p.dial(r.Context())
	if err != nil {
		http.Error(w, "Upstream proxy unavailable", http.StatusBadGateway)
		return
	}
	defer p.release(conn)
	_ = conn.SetDeadline(time.Now().Add(proxyRequestTimeout))
	request := r.Clone(r.Context())
	request.RequestURI = ""
	request.Host = request.URL.Host
	request.Header = r.Header.Clone()
	removeProxyHopHeaders(request.Header)
	// The local HTTP server handles the client's 100-continue expectation when
	// its body is read. WriteProxy sends the body without an upstream handshake.
	request.Header.Del("Expect")
	request.Header.Set("Proxy-Authorization", p.config.authorization)
	request.Close = true
	request.Body = http.MaxBytesReader(w, r.Body, proxyRequestBodyLimit)
	if err = request.WriteProxy(conn); err != nil {
		http.Error(w, "Upstream request failed", http.StatusBadGateway)
		return
	}
	reader := bufio.NewReaderSize(conn, 8192)
	response, err := readFinalProxyResponse(reader, request)
	if err != nil {
		http.Error(w, "Upstream response failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode == http.StatusProxyAuthRequired {
		http.Error(w, "Upstream response rejected", http.StatusBadGateway)
		return
	}
	removeProxyHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if _, err = io.Copy(w, response.Body); err != nil {
		panic(http.ErrAbortHandler)
	}
}
func (p *EgressProxy) connect(w http.ResponseWriter, r *http.Request) {
	upstream, err := p.dial(r.Context())
	if err != nil {
		http.Error(w, "Upstream proxy unavailable", http.StatusBadGateway)
		return
	}
	defer p.release(upstream)
	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: r.Host}, Host: r.Host, Header: http.Header{"Proxy-Authorization": {p.config.authorization}}}
	if err = request.Write(upstream); err != nil {
		http.Error(w, "Upstream CONNECT failed", http.StatusBadGateway)
		return
	}
	upstreamReader := bufio.NewReaderSize(upstream, 8192)
	header, err := readProxyResponseHeader(upstreamReader)
	if err != nil {
		http.Error(w, "Upstream CONNECT failed", http.StatusBadGateway)
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), request)
	if err != nil || response.StatusCode != http.StatusOK {
		http.Error(w, "Upstream CONNECT rejected", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Tunneling unavailable", http.StatusInternalServerError)
		return
	}
	downstream, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if !p.track(downstream) {
		return
	}
	defer p.release(downstream)
	deadline := time.Now().Add(proxyTunnelLifetime)
	_ = upstream.SetDeadline(deadline)
	_ = downstream.SetDeadline(deadline)
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if _, err := io.Copy(downstream, upstreamReader); err != nil {
			_ = upstream.Close()
			_ = downstream.Close()
			return
		}
		closeProxyWrite(downstream)
	}()
	if _, err = io.Copy(upstream, buffer.Reader); err != nil {
		_ = upstream.Close()
		_ = downstream.Close()
	} else {
		closeProxyWrite(upstream)
	}
	<-finished
}
func closeProxyWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	} else {
		_ = conn.Close()
	}
}
