package setup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
)

// systemdDetected returns true if systemd appears to be the init system.
func systemdDetected() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd"); err != nil {
		return false
	}
	return true
}

func CreateGroup(name string) error {
	fmt.Fprintf(os.Stderr, "Creating group %s...\n", name)
	if out, err := exec.Command("groupadd", "-r", name).CombinedOutput(); err != nil {
		if IsExitError(err) {
			fmt.Fprintf(os.Stderr, "  Group %s already exists\n", name)
			return nil
		}
		return fmt.Errorf("groupadd %s: %s", name, out)
	}
	return nil
}

func CreateSystemUser(name, group, desc, home string) error {
	fmt.Fprintf(os.Stderr, "Creating user %s...\n", name)
	args := []string{"-r", "-g", group, "-s", "/usr/sbin/nologin", "-d", home, "-c", desc, name}
	if out, err := exec.Command("useradd", args...).CombinedOutput(); err != nil {
		if IsExitError(err) {
			fmt.Fprintf(os.Stderr, "  User %s already exists\n", name)
			return nil
		}
		return fmt.Errorf("useradd %s: %s", name, out)
	}
	return nil
}

func CreateDir(path string, mode os.FileMode) error {
	fmt.Fprintf(os.Stderr, "Creating directory %s...\n", path)
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return nil
}

func ChownDir(path, owner, group string) error {
	uid, gid, err := lookupUserGroup(owner, group)
	if err != nil {
		return fmt.Errorf("lookup %s:%s: %w", owner, group, err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s %s:%s: %w", path, owner, group, err)
	}
	return nil
}

func WriteConfig(path string, data []byte, mode os.FileMode, owner, group string) error {
	fmt.Fprintf(os.Stderr, "Creating config at %s...\n", path)
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return ChownFile(path, owner, group)
}

func ChownFile(path, owner, group string) error {
	uid, gid, err := lookupUserGroup(owner, group)
	if err != nil {
		return fmt.Errorf("lookup %s:%s: %w", owner, group, err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s %s:%s: %w", path, owner, group, err)
	}
	return nil
}

func InstallService(unitPath string, content []byte) error {
	switch WithSystemd {
	case "yes":
		// proceed
	case "auto":
		if !systemdDetected() {
			fmt.Fprintln(os.Stderr, "Systemd not detected, skipping service installation")
			return nil
		}
	default: // "no" or any other value
		fmt.Fprintln(os.Stderr, "Systemd support disabled, skipping service installation")
		return nil
	}
	fmt.Fprintf(os.Stderr, "Installing systemd service %s...\n", unitPath)
	if err := os.WriteFile(unitPath, content, 0644); err != nil {
		return fmt.Errorf("write %s: %w", unitPath, err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s", out)
	}
	return nil
}

func IsExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func lookupUserGroup(owner, group string) (int, int, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, fmt.Errorf("lookup group %q: %w", group, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("parse gid %q: %w", g.Gid, err)
	}

	if owner == "root" {
		return 0, gid, nil
	}

	u, err := user.Lookup(owner)
	if err != nil {
		return 0, 0, fmt.Errorf("lookup user %q: %w", owner, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("parse uid %q: %w", u.Uid, err)
	}
	return uid, gid, nil
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
