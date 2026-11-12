// Command mergeroles reports which process roles a change affects (S19 9g), from the module map and import graph in internal/arch.
//
//	mergeroles -range BASE...HEAD    the roles one change affects (a PR, or a merge: SHA^1..SHA)
//	mergeroles -since 2026-11-04     each first-parent commit on HEAD since the date, and the ingest-affecting figure
//	mergeroles -from v0.41.9         the same for the first-parent commits after a ref (a release's notes)
//
// Every merge is classified against the graph of the checked-out tree, not the graph at that merge: close enough for a week of history.
//
// Run it from inside the repository; it reads the graph of the checked-out tree.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/apohor/rivolt/internal/arch"
)

func main() {
	rng := flag.String("range", "", "a git diff range: the roles it affects")
	since := flag.String("since", "", "list the first-parent commits on HEAD since this date (git --since) and the ingest-affecting figure")
	until := flag.String("until", "", "with -since: up to this date")
	from := flag.String("from", "", "list the first-parent commits on HEAD after this ref and the ingest-affecting figure")
	flag.Parse()
	given := 0
	for _, v := range []string{*rng, *since, *from} {
		if v != "" {
			given++
		}
	}
	if given != 1 {
		fmt.Fprintln(os.Stderr, "mergeroles: give one of -range, -since and -from")
		os.Exit(2)
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		fail(err)
	}
	m, err := arch.NewRoleMap(strings.TrimSpace(root))
	if err != nil {
		fail(err)
	}
	if *rng != "" {
		roles, err := rolesOf(m, *rng)
		if err != nil {
			fail(err)
		}
		fmt.Println(label(roles))
		return
	}
	args := []string{"log", "--first-parent", "--format=%H %s"}
	period := "since " + *from
	if *from != "" {
		args = append(args, *from+"..HEAD")
	} else {
		args = append(args, "--since="+*since)
		period = "since " + *since
		if *until != "" {
			args = append(args, "--until="+*until)
			period += " until " + *until
		}
		args = append(args, "HEAD")
	}
	out, err := git(args...)
	if err != nil {
		fail(err)
	}
	pr := regexp.MustCompile(`^Merge pull request #(\d+)`)
	var merges [][]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		sha, subject, _ := strings.Cut(line, " ")
		roles, err := rolesOf(m, sha+"^1.."+sha)
		if err != nil {
			fail(err)
		}
		merges = append(merges, roles)
		name := sha[:8]
		if g := pr.FindStringSubmatch(subject); g != nil {
			name = "#" + g[1]
		}
		fmt.Printf("%s\t%s\n", name, label(roles))
	}
	fmt.Printf("Ingest-affecting merges %s: %d of %d\n", period, arch.IngestAffecting(merges), len(merges))
}

func rolesOf(m *arch.RoleMap, rng string) ([]string, error) {
	out, err := git("diff", "--name-only", "--no-renames", rng)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return m.For(paths), nil
}

func label(roles []string) string {
	if len(roles) == 0 {
		return "none"
	}
	return strings.Join(roles, ",")
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mergeroles:", err)
	os.Exit(1)
}
