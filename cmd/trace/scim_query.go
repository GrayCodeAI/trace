package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// scimError writes an RFC 7644 section 3.12 error response.
func scimError(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"}, "status": strconv.Itoa(status), "detail": detail}
	if scimType != "" {
		body["scimType"] = scimType
	}
	w.Header().Set("Content-Type", "application/scim+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

var scimEqFilterPattern = regexp.MustCompile(`^\s*([A-Za-z.]+)\s+(?i:eq)\s+"((?:[^"\\]|\\.)*)"\s*$`)

// parseSCIMEqualityFilter supports the filter identity providers use to look
// resources up: `ATTRIBUTE eq "value"` for one of the allowed attributes
// (compared case-insensitively). An empty filter matches everything.
func parseSCIMEqualityFilter(filter string, attributes ...string) (string, bool, error) {
	if strings.TrimSpace(filter) == "" {
		return "", false, nil
	}
	match := scimEqFilterPattern.FindStringSubmatch(filter)
	if match == nil {
		return "", false, errors.New(`only filters of the form ATTRIBUTE eq "value" are supported`)
	}
	for _, attribute := range attributes {
		if strings.EqualFold(match[1], attribute) {
			var value string
			if err := json.Unmarshal([]byte(`"`+match[2]+`"`), &value); err != nil {
				return "", false, errors.New("invalid filter value")
			}
			return value, true, nil
		}
	}
	return "", false, errors.New("unsupported filter attribute " + match[1])
}

// scimPage applies RFC 7644 section 3.4.2.4 pagination (1-based startIndex,
// non-negative count) to n sorted resources and returns the slice bounds.
func scimPage(r *http.Request, n int) (start, end, startIndex int, err error) {
	startIndex, count := 1, n
	if raw := r.URL.Query().Get("startIndex"); raw != "" {
		value, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, 0, 0, errors.New("startIndex must be an integer")
		}
		if value > 1 {
			startIndex = value
		}
	}
	if raw := r.URL.Query().Get("count"); raw != "" {
		value, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, 0, 0, errors.New("count must be an integer")
		}
		if value < 0 {
			value = 0
		}
		count = value
	}
	start = startIndex - 1
	if start > n {
		start = n
	}
	end = start + count
	if end > n || end < start {
		end = n
	}
	return start, end, startIndex, nil
}

func scimListResponse(total, startIndex int, resources any, itemsPerPage int) map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, "totalResults": total, "startIndex": startIndex, "itemsPerPage": itemsPerPage, "Resources": resources}
}

type scimPatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

var scimMemberPathPattern = regexp.MustCompile(`^\s*(?i:members)\s*\[\s*(?i:value)\s+(?i:eq)\s+"((?:[^"\\]|\\.)*)"\s*\]\s*$`)

// scimMembersValue decodes a members value given either as an array of
// members or as an object with a "members" attribute.
func scimMembersValue(raw json.RawMessage) ([]scimGroupMember, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var members []scimGroupMember
	if err := json.Unmarshal(raw, &members); err == nil {
		return members, nil
	}
	var object struct {
		Members []scimGroupMember `json:"members"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("members value must be a list of {\"value\":USER}")
	}
	return object.Members, nil
}

// scimActiveValue extracts the active flag from a user PATCH operation,
// accepting {"active":false} with no path, or path "active" with a boolean or
// the string "True"/"False" that some providers send.
func scimActiveValue(operation scimPatchOperation) (bool, bool, error) {
	if strings.EqualFold(strings.TrimSpace(operation.Path), "active") {
		var flag bool
		if err := json.Unmarshal(operation.Value, &flag); err == nil {
			return flag, true, nil
		}
		var text string
		if err := json.Unmarshal(operation.Value, &text); err == nil && (strings.EqualFold(text, "true") || strings.EqualFold(text, "false")) {
			return strings.EqualFold(text, "true"), true, nil
		}
		return false, false, errors.New("active must be a boolean")
	}
	if strings.TrimSpace(operation.Path) != "" {
		return false, false, nil
	}
	var object struct {
		Active *bool `json:"active"`
	}
	if err := json.Unmarshal(operation.Value, &object); err != nil || object.Active == nil {
		return false, false, nil
	}
	return *object.Active, true, nil
}
