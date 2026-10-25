package kratos

import (
	"strings"
	"unicode/utf8"
)

// Kratos applies its password policy in the self-service flows (sign-up, settings, recovery), not to a password set through the
// admin API: a 6-character password went straight through SetPassword on preview (qa, Oct 25). Rivolt's password changes use the
// admin API, so the same rules are enforced here, before the write, for every caller. They are Kratos' defaults, from
// selfservice/strategy/password/validator.go: a minimum of 8 characters (counted in characters here, where Kratos counts bytes), a maximum of 72 bytes
// (bcrypt's limit; beyond it the tail is silently ignored), and the identifier-similarity check against the account's email. Our
// Kratos has haveibeenpwned off, so the breach check is not part of it.
const (
	// MinPasswordChars is Kratos' default min_password_length. Kratos counts it in bytes; Rivolt counts characters (runes), which is
	// stricter for non-ASCII and matches the words on the screen ("at least 8 characters"): three CJK characters are 9 bytes and
	// would pass a byte count.
	MinPasswordChars = 8
	// MaxPasswordBytes is bcrypt's limit.
	MaxPasswordBytes = 72

	// The similarity check, Kratos' constants: too close when the edit distance is under minDistance, or when the longest common
	// substring is more than substrThreshold of the password. Skipped for operands longer than maxSimilarityLen, as Kratos does.
	minDistance      = 5
	substrThreshold  = 0.5
	maxSimilarityLen = 256
)

// CheckPassword applies the policy: "" when the password is acceptable, else a rejection code (RejectTooShort, RejectTooLong,
// RejectTooSimilar).
func CheckPassword(password, identifier string) string {
	if utf8.RuneCountInString(password) < MinPasswordChars {
		return RejectTooShort
	}
	if len(password) > MaxPasswordBytes {
		return RejectTooLong
	}
	id, pw := strings.ToLower(identifier), strings.ToLower(password)
	if id != "" && len(identifier) <= maxSimilarityLen && len(password) <= maxSimilarityLen && len(id) <= maxSimilarityLen && len(pw) <= maxSimilarityLen {
		if levenshtein(id, pw) < minDistance || float32(longestCommonSubstring(id, pw))/float32(utf8.RuneCountInString(pw)) > substrThreshold {
			return RejectTooSimilar
		}
	}
	return ""
}

// ReasonFor is the plain-language text for a code, in Kratos' own wording, for logs and support; screens use the code.
func ReasonFor(code string) string {
	switch code {
	case RejectTooShort:
		return "The password must be at least 8 characters long."
	case RejectTooLong:
		return "The password must be at most 72 characters long."
	case RejectTooSimilar:
		return "The password can not be used because it is too similar to the identifier."
	}
	return "The password is not accepted."
}

// levenshtein is the edit distance between two strings, by runes.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// longestCommonSubstring is the length, in runes, of the longest run the two strings share.
func longestCommonSubstring(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	best := 0
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			if ra[i-1] == rb[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > best {
					best = cur[j]
				}
			} else {
				cur[j] = 0
			}
		}
		prev, cur = cur, prev
	}
	return best
}
