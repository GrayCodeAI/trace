package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/GrayCodeAI/trace/cli/api"
)

func recapLoadErrorMessage(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Recap request was canceled."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("Recap request timed out. Check your internet connection and retry. Details: %v", err)
	}

	var apiErr *api.HTTPError
	if errors.As(err, &apiErr) {
		detail := recapErrorDetail(apiErr)
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return "Run `trace login` to re-authenticate."
		case http.StatusBadRequest:
			return "Entire sent an invalid recap time range. Please update Trace CLI and retry. Details: " + detail
		case http.StatusNotFound:
			return "trace.io could not find your account. Run `trace logout` then `trace login`; if it still fails, contact Trace support. Details: " + detail
		default:
			if apiErr.StatusCode >= http.StatusInternalServerError {
				return "trace.io could not build the recap. Please retry in a moment; if it still fails, contact Trace support. Details: " + detail
			}
			return err.Error()
		}
	}
	if host, ok := dnsNotFoundHost(err); ok {
		return fmt.Sprintf("Could not resolve API host %q (DNS lookup failed). Check TRACE_API_BASE_URL — the host may be misspelled or the env var may be pointing at a non-existent server. Details: %v", host, err)
	}
	if isRecapNetworkError(err) {
		return fmt.Sprintf("Could not reach trace.io. Check your internet connection and TRACE_API_BASE_URL if you use a custom API host. Details: %v", err)
	}
	return err.Error()
}

// dnsNotFoundHost reports an NXDOMAIN-style failure, distinguishing "host
// doesn't exist" from generic connectivity loss.
func dnsNotFoundHost(err error) (string, bool) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return dnsErr.Name, true
	}
	return "", false
}

func recapErrorDetail(err *api.HTTPError) string {
	if strings.TrimSpace(err.Message) != "" {
		return fmt.Sprintf("HTTP %d: %s", err.StatusCode, err.Message)
	}
	if text := http.StatusText(err.StatusCode); text != "" {
		return fmt.Sprintf("HTTP %d: %s", err.StatusCode, text)
	}
	return fmt.Sprintf("HTTP %d", err.StatusCode)
}

func isRecapNetworkError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
