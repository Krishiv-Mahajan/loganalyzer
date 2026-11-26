package analysis

import (
	"regexp"
	"strings"
)

// Placeholders substituted for variable parts of a message.
const (
	phString = "<str>"
	phUUID   = "<uuid>"
	phIP     = "<ip>"
	phHex    = "<hex>"
	phNumber = "<n>"
)

var (
	reQuoted = regexp.MustCompile(`"[^"]*"|'[^'\s]*'`)
	reUUID   = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reIPv4   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	reHexLit = regexp.MustCompile(`\b0[xX][0-9a-fA-F]+\b`)
	reHexID  = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	reNumber = regexp.MustCompile(`\d+(?:\.\d+)?`)
)

// Normalize reduces a log message to a pattern by replacing variable
// components (quoted strings, UUIDs, IPv4 addresses, hex values and IDs, and
// numbers) with placeholders and collapsing runs of whitespace. Messages that
// differ only in such values, for example
//
//	connection to 10.0.0.1:5432 timed out after 30s
//	connection to 10.0.0.7:5432 timed out after 45s
//
// normalize to the same pattern:
//
//	connection to <ip>:<n> timed out after <n>s
func Normalize(msg string) string {
	if !strings.ContainsAny(msg, "0123456789\"'") {
		return collapseSpace(msg)
	}
	msg = reQuoted.ReplaceAllString(msg, phString)
	msg = reUUID.ReplaceAllString(msg, phUUID)
	msg = reIPv4.ReplaceAllString(msg, phIP)
	msg = reHexLit.ReplaceAllString(msg, phHex)
	msg = reHexID.ReplaceAllStringFunc(msg, func(s string) string {
		// Only treat mixed letter/digit runs as IDs; pure digits are handled
		// as numbers below and pure letters ("deadbeef") are left alone.
		if strings.ContainsAny(s, "0123456789") && strings.ContainsAny(s, "abcdefABCDEF") {
			return phHex
		}
		return s
	})
	msg = reNumber.ReplaceAllString(msg, phNumber)
	return collapseSpace(msg)
}

// collapseSpace replaces runs of whitespace with a single space. It returns
// s unchanged (without allocating) when there is nothing to collapse.
func collapseSpace(s string) string {
	prevSpace := true // also trims leading space
	clean := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		isSpace := c == ' ' || c == '\t'
		if isSpace && (prevSpace || c == '\t') {
			clean = false
			break
		}
		prevSpace = isSpace
	}
	if clean && !prevSpace {
		return s
	}
	return strings.Join(strings.Fields(s), " ")
}
