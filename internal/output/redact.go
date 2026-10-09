package output

import "regexp"

var secretParam = regexp.MustCompile(`(?i)((?:api_token|access_token|refresh_token|token)=)[^&\s"]+`)

// Redact hides token values in URLs and messages, such as the request URL
// Go includes in a connection error.
func Redact(s string) string {
	return secretParam.ReplaceAllString(s, "${1}REDACTED")
}
