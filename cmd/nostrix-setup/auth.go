package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// accessVerifier verifies Cloudflare Access-issued JWTs against the Access
// JWKS for a given team domain and application audience. The web UI is only
// ever reached through a Cloudflare Tunnel gated by Access, but a tunnel's
// public hostname and its Access policy are configured independently in
// Cloudflare — verifying here means a missing or misconfigured Access
// policy fails closed instead of silently exposing the app.
type accessVerifier struct {
	teamDomain string
	aud        string
	httpClient *http.Client

	// certsURLOverride replaces the derived Cloudflare certs URL when set,
	// so tests can point at a local JWKS server.
	certsURLOverride string

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func newAccessVerifier(teamDomain, aud string) *accessVerifier {
	return &accessVerifier{
		teamDomain: teamDomain,
		aud:        aud,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

const (
	jwksTTL   = time.Hour
	clockSkew = 60 * time.Second
)

type jwk struct {
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

// stringOrSlice unmarshals a JSON value that may be either a single string
// or an array of strings — JWT claims like "aud" are commonly either.
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err != nil {
		return err
	}
	*s = multi
	return nil
}

type accessClaims struct {
	Aud   stringOrSlice `json:"aud"`
	Exp   int64         `json:"exp"`
	Iat   int64         `json:"iat"`
	Iss   string        `json:"iss"`
	Email string        `json:"email"`
}

func (v *accessVerifier) certsURL() string {
	if v.certsURLOverride != "" {
		return v.certsURLOverride
	}
	return fmt.Sprintf("https://%s.cloudflareaccess.com/cdn-cgi/access/certs", v.teamDomain)
}

// keyFor returns the public key for kid, fetching (or refreshing, once
// stale) the JWKS as needed.
func (v *accessVerifier) keyFor(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	key, ok := v.keys[kid]
	stale := time.Since(v.fetchedAt) > jwksTTL
	v.mu.Unlock()

	if ok && !stale {
		return key, nil
	}

	if err := v.refresh(); err != nil {
		if ok {
			// Transient fetch error — fall back to the stale key rather
			// than lock everyone out.
			return key, nil
		}
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	key, ok = v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	return key, nil
}

func (v *accessVerifier) refresh() error {
	resp, err := v.httpClient.Get(v.certsURL())
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching JWKS: unexpected status %s", resp.Status)
	}

	var parsed jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("decoding JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(parsed.Keys))
	for _, k := range parsed.Keys {
		pub, err := jwkToRSAPublicKey(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

func jwkToRSAPublicKey(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decoding modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decoding exponent: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() {
		return nil, fmt.Errorf("exponent too large")
	}

	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// verify checks tokenString's signature, audience, issuer, and expiry,
// returning the authenticated email on success.
func (v *accessVerifier) verify(tokenString string) (string, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed token")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decoding header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return "", fmt.Errorf("parsing header: %w", err)
	}
	if header.Alg != "RS256" {
		return "", fmt.Errorf("unsupported algorithm %q", header.Alg)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decoding signature: %w", err)
	}

	key, err := v.keyFor(header.Kid)
	if err != nil {
		return "", fmt.Errorf("resolving key: %w", err)
	}

	hashed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed[:], sig); err != nil {
		return "", fmt.Errorf("invalid signature")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decoding payload: %w", err)
	}
	var claims accessClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return "", fmt.Errorf("parsing claims: %w", err)
	}

	now := time.Now()
	if claims.Exp == 0 || now.After(time.Unix(claims.Exp, 0).Add(clockSkew)) {
		return "", fmt.Errorf("token expired")
	}
	if claims.Iat != 0 && now.Before(time.Unix(claims.Iat, 0).Add(-clockSkew)) {
		return "", fmt.Errorf("token not yet valid")
	}
	if wantIss := "https://" + v.teamDomain + ".cloudflareaccess.com"; claims.Iss != wantIss {
		return "", fmt.Errorf("unexpected issuer %q", claims.Iss)
	}

	audOK := false
	for _, a := range claims.Aud {
		if a == v.aud {
			audOK = true
			break
		}
	}
	if !audOK {
		return "", fmt.Errorf("token not issued for this application")
	}

	return claims.Email, nil
}

// bootstrapUser/bootstrapPassword protect the LAN-reachable first-boot
// setup form (see serve.go's handleBootstrap) — the same fixed credential
// as the temporary first-boot SSH root password (flake.nix's images.*),
// not real authentication, just a bar against a random LAN page hitting an
// otherwise-open endpoint. Gone once the device has a real config.
const (
	bootstrapUser     = "root"
	bootstrapPassword = "nostrix"
)

// requireBasicAuth wraps next behind a fixed HTTP Basic Auth credential.
func requireBasicAuth(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Nostrix bootstrap setup"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAccess wraps next, rejecting any request without a valid
// Cloudflare Access JWT. Access delivers the JWT either as the
// CF_Authorization cookie (browser flows) or the Cf-Access-Jwt-Assertion
// header.
func requireAccess(v *accessVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Cf-Access-Jwt-Assertion")
		if token == "" {
			if c, err := r.Cookie("CF_Authorization"); err == nil {
				token = c.Value
			}
		}
		if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, err := v.verify(token); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
