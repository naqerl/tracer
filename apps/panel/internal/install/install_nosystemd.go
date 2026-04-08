//go:build !systemd

package install

// maybeInstallSystemd is a no-op when systemd build tag is not enabled.
func maybeInstallSystemd() error {
	return nil
}
