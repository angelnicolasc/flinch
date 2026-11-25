//go:build !windows

package usersetup

func registeredBinary(host, path string) (string, error) {
	return path, nil
}
