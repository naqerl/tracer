// Package installer provides installation metadata and helpers for tracer binaries.
// This ensures single source of truth between install scripts and binaries.
package installer

import (
	"encoding/json"
	"fmt"
)

// BinaryType identifies which tracer binary.
type BinaryType string

const (
	BinaryCollector BinaryType = "tracer-collector"
	BinaryPanel     BinaryType = "tracer-panel"
)

// Metadata describes installation requirements for a binary.
type Metadata struct {
	Binary BinaryType `json:"binary"`
	
	// Service configuration
	Service ServiceConfig `json:"service"`
	
	// File system paths
	Paths Paths `json:"paths"`
	
	// Security
	User  UserConfig `json:"user"`
	Group GroupConfig `json:"group"`
}

// ServiceConfig describes systemd service requirements.
type ServiceConfig struct {
	Description         string   `json:"description"`
	Documentation       string   `json:"documentation"`
	After               []string `json:"after"`
	Wants               []string `json:"wants,omitempty"`
	ExecStart           string   `json:"exec_start"`
	Restart             string   `json:"restart"`
	RestartSec          int      `json:"restart_sec"`
	SupplementaryGroups []string `json:"supplementary_groups,omitempty"`
}

// Paths describes installation paths.
type Paths struct {
	InstallDir  string `json:"install_dir"`  // /usr/local/bin
	ConfigDir   string `json:"config_dir"`   // /etc/tracer
	DataDir     string `json:"data_dir"`     // /var/lib/tracer (panel only)
	StateDir    string `json:"state_dir"`    // systemd StateDirectory
	ConfigFile  string `json:"config_file"`  // relative to ConfigDir
	Permissions Permissions `json:"permissions"`
}

// Permissions for files and directories.
type Permissions struct {
	Binary  int `json:"binary"`  // 0755
	Config  int `json:"config"`  // 0640
	DataDir int `json:"data_dir"` // 0750
}

// UserConfig describes the service user.
type UserConfig struct {
	Name    string `json:"name"`
	HomeDir string `json:"home_dir"`
	Shell   string `json:"shell"`
	Comment string `json:"comment"`
}

// GroupConfig describes the service group.
type GroupConfig struct {
	Name string `json:"name"`
}

// Metadata returns installation metadata for a binary type.
func (bt BinaryType) Metadata() Metadata {
	base := Metadata{
		Binary: bt,
		Paths: Paths{
			InstallDir: "/usr/local/bin",
			ConfigDir:  "/etc/tracer",
			Permissions: Permissions{
				Binary:  0755,
				Config:  0640,
				DataDir: 0750,
			},
		},
		Group: GroupConfig{
			Name: string(bt),
		},
	}
	
	switch bt {
	case BinaryCollector:
		base.Service = ServiceConfig{
			Description:   "Tracer Log Collector",
			Documentation: "https://github.com/ai-shift/tracer",
			After:         []string{"network.target"},
			Restart:       "always",
			RestartSec:    5,
			SupplementaryGroups: []string{"systemd-journal", "adm"},
		}
		base.Paths.ConfigFile = "collector.yaml"
		base.User = UserConfig{
			Name:    string(bt),
			HomeDir: "/nonexistent",
			Shell:   "/usr/sbin/nologin",
			Comment: "Tracer Collector Service",
		}
		
	case BinaryPanel:
		base.Service = ServiceConfig{
			Description:   "Tracer Panel",
			Documentation: "https://github.com/ai-shift/tracer",
			After:         []string{"network.target"},
			Restart:       "always",
			RestartSec:    5,
		}
		base.Paths.DataDir = "/var/lib/tracer"
		base.Paths.StateDir = "tracer"
		base.Paths.ConfigFile = "panel.yaml"
		base.User = UserConfig{
			Name:    string(bt),
			HomeDir: "/var/lib/tracer",
			Shell:   "/usr/sbin/nologin",
			Comment: "Tracer Panel Service",
		}
	}
	
	return base
}

// SecurityHardening returns common systemd security hardening options.
func (m Metadata) SecurityHardening() map[string]string {
	return map[string]string{
		"NoNewPrivileges":       "yes",
		"ProtectSystem":         "strict",
		"ProtectHome":           "yes",
		"PrivateTmp":            "yes",
		"ProtectKernelTunables": "yes",
		"ProtectKernelModules":  "yes",
		"ProtectControlGroups":  "yes",
	}
}

// GenerateSystemdUnit generates a systemd service file content.
func (m Metadata) GenerateSystemdUnit() string {
	deps := ""
	if len(m.Service.After) > 0 {
		deps = fmt.Sprintf("After=%s\n", join(m.Service.After))
	}
	if len(m.Service.Wants) > 0 {
		deps += fmt.Sprintf("Wants=%s\n", join(m.Service.Wants))
	}
	
	security := ""
	for k, v := range m.SecurityHardening() {
		security += fmt.Sprintf("%s=%s\n", k, v)
	}
	
	suppGroups := ""
	if len(m.Service.SupplementaryGroups) > 0 {
		suppGroups = fmt.Sprintf("SupplementaryGroups=%s\n", join(m.Service.SupplementaryGroups))
	}
	
	stateDir := ""
	if m.Paths.StateDir != "" {
		stateDir = fmt.Sprintf("StateDirectory=%s\nStateDirectoryMode=0750\n", m.Paths.StateDir)
	}
	
	return fmt.Sprintf(`[Unit]
Description=%s
Documentation=%s
%s
[Service]
Type=simple
User=%s
Group=%s
%sExecStart=%s/%s -config %s/%s
Restart=%s
RestartSec=%d

# Config setup
ConfigurationDirectory=tracer
ConfigurationDirectoryMode=0755
%s
# Security hardening
%s
[Install]
WantedBy=multi-user.target
`,
		m.Service.Description,
		m.Service.Documentation,
		deps,
		m.User.Name,
		m.Group.Name,
		suppGroups,
		m.Paths.InstallDir,
		m.Binary,
		m.Paths.ConfigDir,
		m.Paths.ConfigFile,
		m.Service.Restart,
		m.Service.RestartSec,
		stateDir,
		security,
	)
}

// JSON returns the metadata as JSON.
func (m Metadata) JSON() string {
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}

func join(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += " "
		}
		result += v
	}
	return result
}

// CollectorMetadata returns metadata for tracer-collector.
func CollectorMetadata() Metadata {
	return BinaryCollector.Metadata()
}

// PanelMetadata returns metadata for tracer-panel.
func PanelMetadata() Metadata {
	return BinaryPanel.Metadata()
}
