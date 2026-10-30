//go:build !linux && !darwin

package loggroup

func lookup(string) (int, bool, error) { return 0, false, ErrUnsupported }

func create(string) error { return ErrUnsupported }
