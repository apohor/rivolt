package iosapp

import "testing"

func TestClean(t *testing.T) {
	ok := []string{
		"https://apps.apple.com/us/app/rivolt/id1234567890",
		"https://testflight.apple.com/join/AbCdEf12",
		"  https://apps.apple.com/app/id1234567890  ",
	}
	for _, s := range ok {
		if Clean(s) == "" {
			t.Errorf("%q should be accepted", s)
		}
	}
	bad := []string{
		"", "http://apps.apple.com/app/id1234567890", "https://evil.example/app/id1234567890",
		"https://apps.apple.com.evil.example/app/id1234567890", "https://apps.apple.com/app/id1234567890?utm_source=email",
		"https://apps.apple.com/app/id1234567890#x", "https://user:pw@apps.apple.com/app/id1234567890",
		"https://apps.apple.com:8443/app/id1234567890", "https://apps.apple.com/", "https://apps.apple.com",
		"//apps.apple.com/app/id1234567890", "javascript:alert(1)", "https://testflight.apple.com/join/x?t=1",
	}
	for _, s := range bad {
		if got := Clean(s); got != "" {
			t.Errorf("%q should be refused, got %q", s, got)
		}
	}
}

func TestAppStoreID(t *testing.T) {
	if got := AppStoreID("https://apps.apple.com/us/app/rivolt/id1234567890"); got != "1234567890" {
		t.Errorf("id = %q", got)
	}
	if got := AppStoreID("https://testflight.apple.com/join/AbCdEf12"); got != "" {
		t.Errorf("TestFlight has no app id, got %q", got)
	}
	if got := AppStoreID("https://apps.apple.com/us/app/rivolt"); got != "" {
		t.Errorf("no id in the path, got %q", got)
	}
}

func TestURLReadsTheSetting(t *testing.T) {
	t.Setenv(EnvURL, "")
	if URL() != "" {
		t.Fatal("unset should be empty")
	}
	t.Setenv(EnvURL, "https://testflight.apple.com/join/AbCdEf12")
	if URL() != "https://testflight.apple.com/join/AbCdEf12" {
		t.Fatalf("got %q", URL())
	}
}
