//go:build !darwin && !linux

package testbin

import "errors"

func processIDs() []int { return nil }

func processExecutable(int) (string, error) {
	return "", errors.New("process executable lookup unsupported")
}
