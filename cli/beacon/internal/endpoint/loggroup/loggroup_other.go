//go:build !linux && !darwin && !windows

package loggroup

func lookup(string) (int, bool, error) { return 0, false, ErrUnsupported }

func create(string) error { return ErrUnsupported }
