// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package localserver secures an HTTP handler intended for local browser access.
package localserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// SessionCookieName is the cookie used to authenticate local browser requests.
const SessionCookieName = "releaseui_session"

// Session authenticates requests to one local HTTP server instance.
type Session struct {
	token string
}

// NewSession creates a local HTTP session with a random authentication token.
func NewSession() (*Session, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	return &Session{token: base64.RawURLEncoding.EncodeToString(tokenBytes)}, nil
}

// LaunchURL adds the one-time session token to baseURL.
func (s *Session) LaunchURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
		return "", fmt.Errorf("base URL must be an HTTP or HTTPS origin, got %q", baseURL)
	}
	query := parsed.Query()
	query.Set("token", s.token)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// Protect restricts next to loopback requests authenticated through the launch URL.
func (s *Session) Protect(next http.Handler) http.Handler {
	return securityHeaders(s.authenticate(next))
}

func (s *Session) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !IsLoopbackHost(request.Host) {
			writeError(response, http.StatusMisdirectedRequest, "release UI only accepts loopback hosts")
			return
		}
		if cookie, err := request.Cookie(SessionCookieName); err == nil && Equal(cookie.Value, s.token) {
			next.ServeHTTP(response, request)
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/" && Equal(request.URL.Query().Get("token"), s.token) {
			http.SetCookie(response, &http.Cookie{
				Name: SessionCookieName, Value: s.token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(response, request, "/", http.StatusSeeOther)
			return
		}
		writeError(response, http.StatusUnauthorized, "open the launch URL printed by releaseui")
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

// IsLoopbackHost reports whether hostport names a loopback host.
func IsLoopbackHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SameOrigin reports whether a request came from its own HTTP origin.
func SameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return parsed.Scheme == scheme && parsed.Host == request.Host && parsed.Path == ""
}

// Equal compares authentication and release intent values in constant time.
func Equal(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func writeError(response http.ResponseWriter, status int, message string) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"error": message})
}
