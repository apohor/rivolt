package onboardmail

import (
	"fmt"

	"github.com/apohor/rivolt/internal/iosapp"
)

// Links are the URLs an email may carry. App and Demo are empty when the
// install has no base URL configured.
type Links struct {
	App, Demo, Unsubscribe string
	// IOSApp is the iPhone app link (RIVOLT_IOS_APP_URL); empty to leave the line out.
	IOSApp string
}

// Content is one email to send.
type Content struct {
	Subject, Text  string
	Key            string // idempotency key prefix (Resend dedupes for 24 h)
	Attempt        int
	UnsubscribeURL string
}

const connectParagraph = "You'll sign in with your Rivian account (the same login you use in the Rivian app). " +
	"Rivolt only reads your vehicle data; it never sends commands to your car, and your Rivian password is never stored."

func linkOrName(l Links) string {
	if l.App == "" {
		return "your Rivolt account"
	}
	return l.App
}

func footer(l Links) string {
	return "\n--\nYou can stop these emails here: " + l.Unsubscribe + "\n" +
		"(or in Settings, under Notifications). Delete your account any time from Settings.\n"
}

// Welcome is the day-0 email.
func Welcome(l Links) Content {
	text := "Welcome to Rivolt.\n\n" +
		"Rivolt records your Rivian's drives and charging and shows what they cost and how efficiently you drive.\n\n" +
		"The one step that matters: connect your Rivian. It takes about two minutes.\n\n" +
		"  " + linkOrName(l) + "\n\n" + connectParagraph + "\n"
	if l.Demo != "" {
		text += "\nWant to look around first? There is a demo with a sample truck: " + l.Demo + "\n"
	}
	if l.IOSApp != "" {
		text += "\n" + iosapp.Line + l.IOSApp + "\n"
	}
	text += footer(l)
	return Content{Subject: "Welcome to Rivolt", Text: text, Key: "welcome", Attempt: 0, UnsubscribeURL: l.Unsubscribe}
}

// Nudge is a setup reminder. kind is db.NudgeNoVehicle or db.NudgeNoData;
// attempt is 1 or 2.
func Nudge(kind string, attempt int, l Links) Content {
	var subject, intro string
	switch {
	case kind == "no_data" && attempt > 1:
		subject = "Still no data from your Rivian in Rivolt"
		intro = "Your Rivian is connected to Rivolt, but nothing has arrived yet, so no drives or charges are being recorded.\n\n" +
			"The usual causes: the account is an Authorized Driver whose invite was not accepted in the Rivian app yet, or the truck has not been driven or woken since you connected. " +
			"Open Rivolt and use \"Check again\" on the Rivian step:\n\n"
	case kind == "no_data":
		subject = "Rivolt is connected, but no data has arrived yet"
		intro = "You connected your Rivian, but Rivolt has not received any data from it yet.\n\n" +
			"The usual causes: the account is an Authorized Driver whose invite was not accepted in the Rivian app yet, or the truck has not been driven or woken since you connected. " +
			"Open Rivolt and use \"Check again\" on the Rivian step:\n\n"
	case attempt > 1:
		subject = "Your Rivolt account is still waiting for your Rivian"
		intro = "A reminder: your Rivolt account is not connected to your Rivian yet, so nothing is being recorded. It is one sign-in.\n\n" +
			"Connect it here:\n\n"
	default:
		subject = "Finish setting up Rivolt: connect your Rivian"
		intro = "You are one step away from seeing your drives, charging and efficiency; connecting your Rivian takes about two minutes.\n\n" +
			"Connect it here:\n\n"
	}
	text := intro + "  " + linkOrName(l) + "\n\n" + connectParagraph + "\n" + footer(l)
	return Content{Subject: subject, Text: text, Key: fmt.Sprintf("setup-%d", attempt), Attempt: attempt, UnsubscribeURL: l.Unsubscribe}
}
