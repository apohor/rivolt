package statement

import (
	"encoding/base64"
	"strconv"
)

func base64URL(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func itoa(v int64) string       { return strconv.FormatInt(v, 10) }
