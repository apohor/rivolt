package zonelookup

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/bradfitz/latlong"
	"github.com/ringsaturn/tzf"
	tzfdist "github.com/ringsaturn/tzf-dist"
	pb "github.com/ringsaturn/tzf/gen/go/tzf/v1"
	"google.golang.org/protobuf/proto"
)

func rssMB() float64 {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "VmRSS:") {
				var kb float64
				fmt.Sscan(strings.TrimSpace(strings.TrimPrefix(l, "VmRSS:")), &kb)
				return kb / 1024
			}
		}
	}
	out, _ := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(os.Getpid())).Output()
	var kb float64
	fmt.Sscan(strings.TrimSpace(string(out)), &kb)
	return kb / 1024
}

func TestRSSProbe(t *testing.T) {
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	t.Logf("before: rss=%.1f MB heap=%.1f MB", rssMB(), float64(m0.HeapAlloc)/1e6)
	switch os.Getenv("PROBE") {
	case "fuzzy":
		pt := &pb.PreindexTimezones{}
		if err := proto.Unmarshal(tzfdist.PreindexData, pt); err != nil {
			t.Fatal(err)
		}
		f, err := tzf.NewFuzzyFinderFromPB(pt)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(f.GetTimezoneName(-104.99, 39.74), f.GetTimezoneName(-117.426, 47.6588), f.GetTimezoneName(-106.485, 31.7619))
	case "latlong":
		for _, c := range [][3]interface{}{{"Denver", 39.7392, -104.9903}, {"Seattle", 47.6205, -122.3493}, {"Spokane", 47.6588, -117.4260}, {"Chicago", 41.8781, -87.6298}, {"NY", 40.7128, -74.0060}, {"Phoenix", 33.4484, -112.0740}, {"Anchorage", 61.2181, -149.9003}, {"Honolulu", 21.3069, -157.8583}, {"Toronto", 43.6532, -79.3832}, {"Tokyo", 35.6762, 139.6503}, {"ElPaso", 31.7619, -106.4850}, {"Hoover", 36.0161, -114.7377}, {"Wendover", 40.7391, -114.0372}, {"Lukeville", 31.88, -112.82}} {
			t.Log(c[0], latlong.LookupZoneName(c[1].(float64), c[2].(float64)))
		}
	case "full":
		f, err := tzf.NewFullFinder()
		if err != nil {
			t.Fatal(err)
		}
		t.Log(f.GetTimezoneName(-104.99, 39.74))
	case "drop":
		f, err := tzf.NewDefaultFinder()
		_ = f
		if err != nil {
			t.Fatal(err)
		}
	default:
		_ = For(39.7392, -104.9903)
	}
	runtime.GC()
	debug.FreeOSMemory()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	t.Logf("after:  rss=%.1f MB heap=%.1f MB", rssMB(), float64(m1.HeapAlloc)/1e6)
}
