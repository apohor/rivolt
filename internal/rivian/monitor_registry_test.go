package rivian

import (
	"context"
	"testing"

	"github.com/apohor/rivolt/internal/drives"
)

func TestComposeDriveCloseRunsEveryHookInOrder(t *testing.T) {
	if composeDriveClose(nil) != nil {
		t.Error("no hooks should give nil")
	}
	var order []string
	mk := func(n string) DriveCloseHook {
		return func(context.Context, drives.Drive) { order = append(order, n) }
	}
	composeDriveClose([]DriveCloseHook{mk("a"), mk("b")})(context.Background(), drives.Drive{})
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("order = %v", order)
	}
}
