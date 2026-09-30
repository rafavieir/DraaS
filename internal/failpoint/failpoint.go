package failpoint

import (
	"context"
	"errors"
	"os"
	"strings"
)

var ErrTriggered = errors.New("draas failpoint triggered")

func Trigger(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw := strings.TrimSpace(os.Getenv("DRAAS_FAILPOINT"))
	if raw == "" {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("DRAAS_FAILPOINT_MODE")))
	for _, item := range strings.Split(raw, ",") {
		if strings.TrimSpace(item) != name {
			continue
		}
		if mode == "exit" || mode == "kill" {
			os.Exit(99)
		}
		return ErrTriggered
	}
	return nil
}
