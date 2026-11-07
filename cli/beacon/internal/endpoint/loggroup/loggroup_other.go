//go:build !linux && !darwin && !windows

package loggroup

func lookup(string) (int, bool, error) { return 0, false, ErrUnsupported }

func create(string) error { return ErrUnsupported }

const accountDomainSeparator = false

func addMember(string, string) error { return ErrUnsupported }
