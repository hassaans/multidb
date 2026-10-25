package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Authenticator resolves the tenant a request acts for. Verifying a JWT or looking up an
// API key happens here, before multidb is involved; multidb trusts the id it is given.
type Authenticator interface {
	Tenant(r *http.Request) (string, error)
}

// StaticTokens maps bearer tokens to tenant ids. Good enough for a demo or a single
// trusted client; replace it with JWT or API-key verification in production.
type StaticTokens map[string]string

// Tenant implements Authenticator.
func (s StaticTokens) Tenant(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || token == "" {
		return "", errors.New("missing bearer token")
	}
	tenant, ok := s[token]
	if !ok {
		return "", errors.New("unknown token")
	}
	return tenant, nil
}

// ParseStaticTokens parses "token=tenant,token=tenant".
func ParseStaticTokens(spec string) (StaticTokens, error) {
	out := StaticTokens{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		token, tenant, ok := strings.Cut(pair, "=")
		token, tenant = strings.TrimSpace(token), strings.TrimSpace(tenant)
		if !ok || token == "" || tenant == "" {
			return nil, fmt.Errorf("expected token=tenant, got %q", pair)
		}
		if _, dup := out[token]; dup {
			return nil, fmt.Errorf("token %q listed twice", token)
		}
		out[token] = tenant
	}
	if len(out) == 0 {
		return nil, errors.New("no tokens configured")
	}
	return out, nil
}
