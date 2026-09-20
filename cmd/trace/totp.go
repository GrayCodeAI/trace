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
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(raw) < 10 {
		return "", errors.New("invalid TOTP secret")
	}
	counter := uint64(at.Unix() / 30)
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
	supplied = strings.TrimSpace(supplied)
	if len(supplied) != 6 {
		return false
	}
	for _, delta := range []int64{-30, 0, 30} {
		want, err := totpCode(secret, now.Add(time.Duration(delta)*time.Second))
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(supplied)) == 1 {
			return true
		}
	}
	return false
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
			u.TOTPSecret, u.TOTPEnabled = secret, true
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
			u.TOTPSecret, u.TOTPEnabled = "", false
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
