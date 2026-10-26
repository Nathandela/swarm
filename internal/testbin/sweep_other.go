//go:build !darwin && !linux

package testbin

import "errors"

func processIDs() ([]int, error) { return nil, errors.New("process enumeration unsupported") }

func processExecutable(int) (string, error) {
	return "", errors.New("process executable lookup unsupported")
}
