package launchnotice

import "github.com/apohor/rivolt/internal/emailshell"

// The HTML parts of the notices: the plain text above, word for word, in the shared Rivolt email shell.

// RenderEmailHTML is the launch notice's HTML part.
func RenderEmailHTML(p Params) string { return emailshell.FromText(Subject, RenderEmail(p)) }

// RenderFoundingEmailHTML is the founding-year-end notice's HTML part.
func RenderFoundingEmailHTML(p FoundingParams) string {
	return emailshell.FromText(FoundingSubject(p), RenderFoundingEmail(p))
}

// RenderRenewalEmailHTML is the renewal reminder's HTML part.
func RenderRenewalEmailHTML(p RenewalParams) string {
	return emailshell.FromText(RenewalSubject(p), RenderRenewalEmail(p))
}
