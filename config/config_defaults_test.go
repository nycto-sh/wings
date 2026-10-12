package config

import "testing"

// The defaults must not collide with those of upstream Wings, so that both can
// run on the same machine.
func TestDefaultsDoNotCollideWithUpstreamWings(t *testing.T) {
	cfg, err := NewAtPath(DefaultLocation)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		value    string
		upstream string
	}{
		{"config location", DefaultLocation, "/etc/pterodactyl/config.yml"},
		{"root directory", cfg.System.RootDirectory, "/var/lib/pterodactyl"},
		{"log directory", cfg.System.LogDirectory, "/var/log/pterodactyl"},
		{"data directory", cfg.System.Data, "/var/lib/pterodactyl/volumes"},
		{"archive directory", cfg.System.ArchiveDirectory, "/var/lib/pterodactyl/archives"},
		{"backup directory", cfg.System.BackupDirectory, "/var/lib/pterodactyl/backups"},
		{"tmp directory", cfg.System.TmpDirectory, "/tmp/pterodactyl"},
		{"username", cfg.System.Username, "pterodactyl"},
		{"passwd directory", cfg.System.Passwd.Directory, "/run/wings/etc"},
		{"machine-id directory", cfg.System.MachineID.Directory, "/run/wings/machine-id"},
		{"network name", cfg.Docker.Network.Name, "pterodactyl_nw"},
		{"network mode", cfg.Docker.Network.Mode, "pterodactyl_nw"},
		{"bridge name", cfg.Docker.Network.BridgeName, "pterodactyl0"},
		{"network interface", cfg.Docker.Network.Interface, "172.18.0.1"},
		{"IPv4 subnet", cfg.Docker.Network.Interfaces.V4.Subnet, "172.18.0.0/16"},
		{"IPv6 subnet", cfg.Docker.Network.Interfaces.V6.Subnet, "fdba:17c8:6c94::/64"},
		{"log file", LogFileName, "wings.log"},
		{"service", ServiceName, "wings.service"},
	}
	for _, tt := range tests {
		if tt.value == "" {
			t.Errorf("%s: expected a default value", tt.name)
		}
		if tt.value == tt.upstream {
			t.Errorf("%s: default %q is the same as upstream Wings", tt.name, tt.value)
		}
	}

	// A Linux interface name can be at most 15 characters long.
	if len(cfg.Docker.Network.BridgeName) > 15 {
		t.Errorf("bridge name %q is longer than 15 characters", cfg.Docker.Network.BridgeName)
	}
}
