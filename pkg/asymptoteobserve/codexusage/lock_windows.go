//go:build windows

package codexusage

func lockState(path string) (func(), error) {
	return func() {}, nil
}
