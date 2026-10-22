//go:build !linux && !darwin

package processcontain

func EnvironmentWriters(_, _ []string) ([]Identity, error) { return nil, ErrUnsupported }
