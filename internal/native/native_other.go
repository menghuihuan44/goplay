//go:build !windows

package native

import (
	"errors"

	"goplay/internal/domain"
)

var ErrUnavailable = errors.New("native: screen capture helper is only implemented on Windows")

type ScreenSource struct{}
type Input struct{}

func PrimaryScreenSize() (int, int)                       { return 1920, 1080 }
func OpenScreenSource(string, int) (*ScreenSource, error) { return nil, ErrUnavailable }
func OpenInput(string, int, int) (*Input, error)          { return nil, ErrUnavailable }
func (s *ScreenSource) Grab() (domain.Frame, error)       { return domain.Frame{}, ErrUnavailable }
func (s *ScreenSource) Describe() string                  { return "unsupported" }
func (s *ScreenSource) Close() error                      { return nil }
func (i *Input) PressKey(string, int) error               { return ErrUnavailable }
func (i *Input) Click(float64, float64, int, int) error   { return ErrUnavailable }
func (i *Input) TypeText(string) error                    { return ErrUnavailable }
