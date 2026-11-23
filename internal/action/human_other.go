//go:build !windows

package action

import (
	"context"
)

// SystemHuman is a no-op on platforms where this build has no native detector.
type SystemHuman struct{}

func NewSystemHuman() *SystemHuman { return &SystemHuman{} }

func (s *SystemHuman) State(ctx context.Context) HumanState { return HumanState{} }
