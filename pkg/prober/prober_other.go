//go:build !linux

package prober

import (
	"context"
)

func (p *Prober) AttributeL2(ctx context.Context, cameraMAC string, learnedTTL int) (bool, error) {
	return false, ErrRawCaptureUnsupported
}

func (p *Prober) LearnTTL(ctx context.Context, cameraMAC string) (int, error) {
	return 0, ErrRawCaptureUnsupported
}
