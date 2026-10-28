//go:build e2e

package harness

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
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// mintInstallationToken exchanges the GitHub App credentials for an installation access token for org. It mirrors
// the exchange in internal/github, whose token is unexported; the harness needs the raw token for the endpoints the
// client does not cover (branch deletion, file deletion).
func mintInstallationToken(ctx context.Context, baseURL, appID, privateKey, org string) (string, error) {
	pemBytes := []byte(privateKey)
	if !strings.HasPrefix(strings.TrimSpace(privateKey), "-----BEGIN") {
		b, err := os.ReadFile(privateKey)
		if err != nil {
			return "", fmt.Errorf("reading private key: %w", err)
		}
		pemBytes = b
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("decoding private key: no PEM block")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err8 != nil {
			return "", fmt.Errorf("parsing private key: %w", err)
		}
		var ok bool
		if key, ok = k8.(*rsa.PrivateKey); !ok {
			return "", fmt.Errorf("private key is not RSA")
		}
	}
	jwt, err := signJWT(appID, key)
	if err != nil {
		return "", err
	}

	instID := os.Getenv("STEERSPEC_INSTALLATION_ID")
	if instID == "" {
		var installs []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		}
		if err := appRequest(ctx, jwt, http.MethodGet, baseURL+"/app/installations", http.StatusOK, &installs); err != nil {
			return "", fmt.Errorf("listing installations: %w", err)
		}
		for _, in := range installs {
			if strings.EqualFold(in.Account.Login, org) {
				instID = strconv.FormatInt(in.ID, 10)
				break
			}
		}
		if instID == "" {
			return "", fmt.Errorf("no app installation for org %q", org)
		}
	}
	var tok struct {
		Token string `json:"token"`
	}
	u := baseURL + "/app/installations/" + instID + "/access_tokens"
	if err := appRequest(ctx, jwt, http.MethodPost, u, http.StatusCreated, &tok); err != nil {
		return "", fmt.Errorf("exchanging installation token: %w", err)
	}
	if tok.Token == "" {
		return "", fmt.Errorf("exchanging installation token: empty token")
	}
	return tok.Token, nil
}

func signJWT(appID string, key *rsa.PrivateKey) (string, error) {
	enc := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("encoding JWT part: %w", err)
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	h, err := enc(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	p, err := enc(map[string]any{"iss": appID, "iat": now - 60, "exp": now + 600})
	if err != nil {
		return "", err
	}
	in := h + "." + p
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func appRequest(ctx context.Context, jwt, method, url string, wantStatus int, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
