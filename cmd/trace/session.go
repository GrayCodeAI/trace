package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed login.html
var loginHTML string

var loginTemplate = template.Must(template.New("login").Parse(loginHTML))

const sessionLifetime = 24 * time.Hour

func secureCookie(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request, message string, status int) {
	nonce, err := newToken()
	if err != nil {
		http.Error(w, "cannot create login form", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "trace_login", Value: nonce, Path: "/login", MaxAge: 600, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r)})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := loginTemplate.Execute(w, struct{ CSRF, Error, OIDCURL string }{CSRF: nonce, Error: message, OIDCURL: oidcLoginLink(a.store)}); err != nil {
		log.Printf("render login: %v", err)
	}
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		a.loginPage(w, r, "Please try again.", http.StatusBadRequest)
		return
	}
	nonce, err := r.Cookie("trace_login")
	if err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(nonce.Value)) != 1 {
		a.loginPage(w, r, "Please refresh the page and try again.", http.StatusForbidden)
		return
	}
	db, err := a.store.loadUsers()
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	name := r.PostForm.Get("username")
	u, ok := db.authenticate(name, r.PostForm.Get("token"))
	if !ok {
		a.loginPage(w, r, "Username or token is incorrect.", http.StatusUnauthorized)
		return
	}
	if u.TOTPEnabled {
		now := time.Now()
		if wait := a.totp.locked(name, now); wait > 0 {
			a.loginPage(w, r, "Too many incorrect two-factor codes. Try again in "+strconv.Itoa(int(wait.Seconds())+1)+" seconds.", http.StatusTooManyRequests)
			return
		}
		step, ok := matchTOTP(u.TOTPSecret, r.PostForm.Get("totp"), now)
		if !ok {
			a.totp.fail(name, now)
			a.loginPage(w, r, "The two-factor code is incorrect.", http.StatusUnauthorized)
			return
		}
		if err := a.store.acceptTOTPCounter(name, step); err != nil {
			a.totp.fail(name, now)
			a.loginPage(w, r, "This two-factor code was already used. Wait for the next code.", http.StatusUnauthorized)
			return
		}
		a.totp.reset(name)
	}
	a.issueSession(w, r, name, u)
	http.SetCookie(w, &http.Cookie{Name: "trace_login", Path: "/login", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r)})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (a *app) sessionMAC(name, expiry, tokenHash string) []byte {
	h := hmac.New(sha256.New, a.sessionKey)
	_, _ = h.Write([]byte("trace-session\x00" + name + "\x00" + expiry + "\x00" + tokenHash))
	return h.Sum(nil)
}

func (a *app) issueSession(w http.ResponseWriter, r *http.Request, name string, u userRecord) {
	expiry := strconv.FormatInt(time.Now().Add(sessionLifetime).Unix(), 10)
	payload := base64.RawURLEncoding.EncodeToString([]byte(name + "|" + expiry))
	signature := base64.RawURLEncoding.EncodeToString(a.sessionMAC(name, expiry, u.Hash))
	http.SetCookie(w, &http.Cookie{Name: "trace_session", Value: payload + "." + signature, Path: "/", MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookie(r)})
}

func (a *app) webIdentity(r *http.Request, db userDB) (string, userRecord, bool) {
	if cookie, err := r.Cookie("trace_session"); err == nil {
		parts := strings.Split(cookie.Value, ".")
		if len(parts) == 2 {
			payload, payloadErr := base64.RawURLEncoding.DecodeString(parts[0])
			mac, macErr := base64.RawURLEncoding.DecodeString(parts[1])
			fields := strings.Split(string(payload), "|")
			if payloadErr == nil && macErr == nil && len(fields) == 2 {
				expiry, parseErr := strconv.ParseInt(fields[1], 10, 64)
				u, exists := db.Users[fields[0]]
				if parseErr == nil && exists && time.Now().Unix() < expiry && subtle.ConstantTimeCompare(mac, a.sessionMAC(fields[0], fields[1], u.Hash)) == 1 {
					return fields[0], a.store.expandUser(fields[0], u), true
				}
			}
		}
	}
	name, token, hasBasic := r.BasicAuth()
	if hasBasic {
		u, ok := db.authenticate(name, token)
		if ok {
			return name, a.store.expandUser(name, u), true
		}
	}
	return "", userRecord{}, false
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	db, err := a.store.loadUsers()
	if err != nil {
		http.Error(w, "cannot load users", http.StatusInternalServerError)
		return
	}
	name, _, ok := a.webIdentity(r, db)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrfFor(name))) != 1 {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "trace_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookie(r)})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
