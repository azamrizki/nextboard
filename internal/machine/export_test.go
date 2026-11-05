package machine

import "testing"

// ConnectTestSSHD is the sshd harness for external tests (package
// machine_test): a throwaway OpenSSH server with SFTP on loopback, added as
// a connected machine of a fresh registry. Callers gate on
// NEXTBOARD_SSHD_TEST themselves.
func ConnectTestSSHD(t *testing.T, extra ...string) (*Registry, *SSH) {
	t.Helper()
	reg := newTestRegistry(t)
	if len(extra) == 0 {
		extra = []string{"Subsystem sftp internal-sftp"}
	}
	return reg, connectTestSSHD(t, reg, startSSHD(t, extra...))
}

// BuildCommandForTest exposes the remote command line builder.
func BuildCommandForTest(m *SSH, spec StartSpec) string { return m.buildCommand(spec) }

// ConnectlessRegistryForTest is a fresh registry (nothing connected).
func ConnectlessRegistryForTest(t *testing.T) *Registry { return newTestRegistry(t) }
