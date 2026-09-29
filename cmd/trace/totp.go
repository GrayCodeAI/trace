package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

func newTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="), nil
}

func totpCode(secret string, at time.Time) (string, error) {
	return totpCodeForCounter(secret, at.Unix()/30)
}

func totpCodeForCounter(secret string, step int64) (string, error) {
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(raw) < 10 {
		return "", errors.New("invalid TOTP secret")
	}
	counter := uint64(step)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	h := hmac.New(sha1.New, raw) // SHA-1 is part of the TOTP standard (RFC 6238).
	_, _ = h.Write(message[:])
	digest := h.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 | uint32(digest[offset+1])<<16 | uint32(digest[offset+2])<<8 | uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1000000), nil
}

func verifyTOTP(secret, supplied string, now time.Time) bool {
	_, ok := matchTOTP(secret, supplied, now)
	return ok
}

// matchTOTP checks a code against the previous, current, and next 30-second
// steps and returns the matching step counter. Callers must also reject
// counters at or below the last one accepted for the account (RFC 6238
// section 5.2) so a code cannot be replayed within the skew window.
func matchTOTP(secret, supplied string, now time.Time) (int64, bool) {
	supplied = strings.TrimSpace(supplied)
	if len(supplied) != 6 {
		return 0, false
	}
	current := now.Unix() / 30
	for _, step := range []int64{current - 1, current, current + 1} {
		want, err := totpCodeForCounter(secret, step)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(supplied)) == 1 {
			return step, true
		}
	}
	return 0, false
}

var errTOTPReplay = errors.New("two-factor code already used")

// acceptTOTPCounter records step as the account's last accepted TOTP step,
// refusing it if an equal or later step was already accepted.
func (s *store) acceptTOTPCounter(name string, step int64) error {
	return s.updateUsers(func(db *userDB) error {
		u, ok := db.Users[name]
		if !ok {
			return errors.New("user not found")
		}
		if step <= u.TOTPLastCounter {
			return errTOTPReplay
		}
		u.TOTPLastCounter = step
		db.Users[name] = u
		return nil
	})
}

// After totpMaxFailures consecutive wrong codes for one account, further
// attempts are refused for an exponentially growing period (30 s doubling,
// at most 15 minutes). Only callers who already presented the account's
// valid token reach the code check, so this cannot be used to lock out a
// user without their token. State is per process and resets on restart.
const (
	totpMaxFailures = 5
	totpBaseLockout = 30 * time.Second
	totpMaxLockout  = 15 * time.Minute
)

type totpAttempt struct {
	failures    int
	lockedUntil time.Time
}

type totpLimiter struct {
	mu       sync.Mutex
	accounts map[string]totpAttempt
}

func newTOTPLimiter() *totpLimiter { return &totpLimiter{accounts: map[string]totpAttempt{}} }

// locked returns how long the account must still wait, or zero.
func (l *totpLimiter) locked(name string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait := l.accounts[name].lockedUntil.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

func (l *totpLimiter) fail(name string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.accounts[name]
	attempt.failures++
	if attempt.failures >= totpMaxFailures {
		lockout := totpBaseLockout << uint(attempt.failures-totpMaxFailures)
		if lockout > totpMaxLockout || lockout <= 0 {
			lockout = totpMaxLockout
		}
		attempt.lockedUntil = now.Add(lockout)
	}
	l.accounts[name] = attempt
}

func (l *totpLimiter) reset(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.accounts, name)
}

func totpCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trace user 2fa <enable|disable|status> [-data DIR] USER")
	}
	fs := flag.NewFlagSet("user 2fa "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: trace user 2fa <enable|disable|status> [-data DIR] USER")
	}
	name := fs.Arg(0)
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	switch args[0] {
	case "enable":
		secret, err := newTOTPSecret()
		if err != nil {
			return err
		}
		if err := s.updateUsers(func(db *userDB) error {
			u, ok := db.Users[name]
			if !ok {
				return errors.New("user not found")
			}
			u.TOTPSecret, u.TOTPEnabled, u.TOTPLastCounter = secret, true, 0
			db.Users[name] = u
			return nil
		}); err != nil {
			return err
		}
		issuer := url.QueryEscape("Trace")
		account := url.QueryEscape(name)
		fmt.Printf("enabled 2FA for %s\nSecret: %s\nProvisioning URI: otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=6&period=30\nStore the secret securely; it will not be shown again.\n", name, secret, issuer, account, secret, issuer)
		return nil
	case "disable":
		return s.updateUsers(func(db *userDB) error {
			u, ok := db.Users[name]
			if !ok {
				return errors.New("user not found")
			}
			u.TOTPSecret, u.TOTPEnabled, u.TOTPLastCounter = "", false, 0
			db.Users[name] = u
			fmt.Println("disabled 2FA for", name)
			return nil
		})
	case "status":
		db, err := s.loadUsers()
		if err != nil {
			return err
		}
		u, ok := db.Users[name]
		if !ok {
			return errors.New("user not found")
		}
		fmt.Printf("%s: 2FA %s\n", name, map[bool]string{true: "enabled", false: "disabled"}[u.TOTPEnabled])
		return nil
	default:
		return errors.New("usage: trace user 2fa <enable|disable|status> [-data DIR] USER")
	}
}
