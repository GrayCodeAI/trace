package cli

// Attribution handles session attribution.
func init() {}

// shortSessionID returns the first 8 characters of a session ID.
func shortSessionID(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[:8]
}
