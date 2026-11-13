package taskbot

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

func GitHubTokenProvider() (func(context.Context) (string, error), error) {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return func(context.Context) (string, error) { return token, nil }, nil
	}
	appID := os.Getenv("GITHUB_APP_ID")
	installation := os.Getenv("GITHUB_INSTALLATION_ID")
	if appID == "" {
		return nil, fmt.Errorf("GITHUB_APP_ID or GITHUB_TOKEN required")
	}
	if id, err := strconv.ParseInt(installation, 10, 64); err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid installation ID")
	}
	raw, err := os.ReadFile(os.Getenv("GITHUB_PRIVATE_KEY_FILE"))
	if err != nil {
		return nil, fmt.Errorf("GitHub private key unreadable")
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("invalid private key PEM")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if err != nil || key == nil {
		return nil, fmt.Errorf("GitHub App requires an RSA private key")
	}
	var mu sync.Mutex
	var token string
	var until time.Time
	client, err := NewExternalHTTPClient(45 * time.Second)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(until) {
			return token, nil
		}
		jwt, err := appJWT(key, appID, time.Now())
		if err != nil {
			return "", err
		}
		var result struct {
			Token string `json:"token"`
		}
		err = requestJSON(ctx, client, http.MethodPost, "https://api.github.com/app/installations/"+installation+"/access_tokens", map[string]any{}, &result, map[string]string{"Authorization": "Bearer " + jwt, "Accept": "application/vnd.github+json", "User-Agent": "ai-taskbot"})
		if err != nil || result.Token == "" {
			return "", fmt.Errorf("installation token unavailable")
		}
		token = result.Token
		until = time.Now().Add(50 * time.Minute)
		return token, nil
	}, nil
}
func appJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Unix() - 60, "exp": now.Unix() + 540, "iss": appID})
	if err != nil {
		return "", err
	}
	message := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
