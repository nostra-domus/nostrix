package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	testTeamDomain = "test-team"
	testAud        = "test-aud"
	testKid        = "test-key-1"
)

func testIssuer() string {
	return "https://" + testTeamDomain + ".cloudflareaccess.com"
}

// newTestVerifier spins up a local JWKS server serving pub under testKid,
// and returns an accessVerifier pointed at it.
func newTestVerifier(t *testing.T, pub *rsa.PublicKey) (*accessVerifier, func()) {
	t.Helper()

	resp := jwksResponse{Keys: []jwk{{
		Kid: testKid,
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(resp)
	}))

	v := newAccessVerifier(testTeamDomain, testAud)
	v.certsURLOverride = srv.URL

	return v, srv.Close
}

// signJWT builds a JWT with the given claims, signed by priv under testKid.
func signJWT(t *testing.T, priv *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()

	header := map[string]any{"alg": "RS256", "kid": testKid, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	signedPart := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	hashed := sha256.Sum256([]byte(signedPart))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		t.Fatal(err)
	}

	return signedPart + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func validClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"aud":   []string{testAud},
		"email": "user@example.com",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Add(-time.Minute).Unix(),
		"iss":   testIssuer(),
	}
}

func TestVerifyAcceptsValidToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, closeSrv := newTestVerifier(t, &priv.PublicKey)
	defer closeSrv()

	token := signJWT(t, priv, validClaims())

	email, err := v.verify(token)
	if err != nil {
		t.Fatalf("expected valid token to verify, got: %v", err)
	}
	if email != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", email)
	}
}

func TestVerifyRejectsBadSignature(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// JWKS advertises priv's public key, but the token is signed by a
	// different key entirely.
	v, closeSrv := newTestVerifier(t, &priv.PublicKey)
	defer closeSrv()

	token := signJWT(t, other, validClaims())

	if _, err := v.verify(token); err == nil {
		t.Fatal("expected forged-signature token to be rejected")
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, closeSrv := newTestVerifier(t, &priv.PublicKey)
	defer closeSrv()

	claims := validClaims()
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	token := signJWT(t, priv, claims)

	if _, err := v.verify(token); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, closeSrv := newTestVerifier(t, &priv.PublicKey)
	defer closeSrv()

	claims := validClaims()
	claims["aud"] = []string{"some-other-application"}
	token := signJWT(t, priv, claims)

	if _, err := v.verify(token); err == nil {
		t.Fatal("expected token for a different application to be rejected")
	}
}

func TestVerifyRejectsUnknownKid(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, closeSrv := newTestVerifier(t, &priv.PublicKey)
	defer closeSrv()

	header := map[string]any{"alg": "RS256", "kid": "no-such-key", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(validClaims())
	signedPart := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	hashed := sha256.Sum256([]byte(signedPart))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	token := signedPart + "." + base64.RawURLEncoding.EncodeToString(sig)

	if _, err := v.verify(token); err == nil {
		t.Fatal("expected token with unknown kid to be rejected")
	}
}

func TestRequireAccessMiddleware(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, closeSrv := newTestVerifier(t, &priv.PublicKey)
	defer closeSrv()

	protected := requireAccess(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No token at all.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// Valid token via cookie.
	token := signJWT(t, priv, validClaims())
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: token})
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid cookie token: status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Valid token via header.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", token)
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid header token: status = %d, want %d", rec.Code, http.StatusOK)
	}
}
