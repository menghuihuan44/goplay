//go:build !windows

package games

import "errors"

// Game mirrors the Windows picker entry so the HTTP contract stays stable on
// every platform.
type Game struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Target string `json:"-"`
}

// ListWindowsGames is empty off Windows; the console hides the picker.
func ListWindowsGames() []Game { return nil }

// LaunchWindowsGame is unavailable off Windows.
func LaunchWindowsGame(string) (Game, error) {
	return Game{}, errors.New("Windows game launching is only available on Windows")
}
