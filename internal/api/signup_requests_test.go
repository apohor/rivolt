package api

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/signuprequests"
)

// The admin list shows that a link was issued, never the token.
func TestAdminRowNeverCarriesTheToken(t *testing.T) {
	tok := "SOMETOKENSOMETOKENSOMETOKE"
	row := toSignupRequestRow(signuprequests.Request{ID: uuid.New(), Email: "a@x.test", Status: "approved", SignupToken: &tok})
	if row.SignupToken == nil || *row.SignupToken == tok || strings.Contains(*row.SignupToken, "TOKEN") {
		t.Errorf("token in the admin row: %v", row.SignupToken)
	}
	none := toSignupRequestRow(signuprequests.Request{ID: uuid.New(), Email: "b@x.test", Status: "pending"})
	if none.SignupToken != nil {
		t.Errorf("pending row has a token marker: %v", none.SignupToken)
	}
}
