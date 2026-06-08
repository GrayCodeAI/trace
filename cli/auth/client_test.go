package auth

import (
	"strings"
	"testing"
)

func TestDecodeJSON_AllowsUnknownFields(t *testing.T) {
	t.Parallel()

	var result DeviceAuthPoll
	err := decodeJSON(strings.NewReader(`{
	  "access_token": "token",
	  "token_type": "Bearer",
	  "refresh_token": "ignored"
	}`), &result)
	if err != nil {
		t.Fatalf("decodeJSON() error = %v", err)
	}

	if result.AccessToken != "token" {
		t.Fatalf("AccessToken = %q, want %q", result.AccessToken, "token")
	}
}

func TestDecodeJSONStrict_RejectsUnknownFields(t *testing.T) {
	t.Parallel()

	var result DeviceAuthStart
	err := decodeJSONStrict(strings.NewReader(`{
	  "device_code": "device",
	  "user_code": "ABCD-EFGH",
	  "verification_uri": "https://example.com/verify",
	  "verification_uri_complete": "https://example.com/verify?code=ABCD-EFGH",
	  "expires_in": 600,
	  "interval": 5,
	  "extra": true
	}`), &result)
	if err == nil {
		t.Fatal("decodeJSONStrict() error = nil, want unknown-field error")
	}
}
