package api

import "testing"

func TestEffectiveVersion(t *testing.T) {
	const stamp = "v0.35.0-dev.24+sha-6dc87c0"
	for _, c := range []struct {
		name, build, pod, want string
	}{
		// prod: a retag of a main build; the deployed release tag is the truth
		{"prod release tag", stamp, "0.34.0", "0.34.0"},
		{"prod v-prefixed", stamp, "v0.34.0", "v0.34.0"},
		{"prod rc", stamp, "0.35.0-rc.1", "0.35.0-rc.1"},
		{"trailing newline from the downward API", stamp, "0.34.0\n", "0.34.0"},
		// preview: image tag is a commit; the build's stamp is more useful
		{"preview sha tag", stamp, "sha-6dc87c0", stamp},
		{"branch tag", stamp, "main", stamp},
		{"latest tag", stamp, "latest", stamp},
		// no stamp: any podinfo tag beats "dev"
		{"unstamped build, sha tag", "dev", "sha-6dc87c0", "sha-6dc87c0"},
		{"empty build, sha tag", "", "sha-6dc87c0", "sha-6dc87c0"},
		// no podinfo (raw docker run / local dev): the stamp
		{"no podinfo", stamp, "", stamp},
		{"no podinfo, unstamped", "dev", "", "dev"},
		{"whitespace-only podinfo", stamp, " \n", stamp},
		// look-alikes are not release tags
		{"four-part numbers", stamp, "1.2.3.4", stamp},
		{"semver-ish word", stamp, "v1.2", stamp},
	} {
		if got := effectiveVersion(c.build, c.pod); got != c.want {
			t.Errorf("%s: effectiveVersion(%q, %q) = %q, want %q", c.name, c.build, c.pod, got, c.want)
		}
	}
}
