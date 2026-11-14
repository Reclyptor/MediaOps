package cli

import (
	"fmt"
	"os"
)

// The disc tools run in one pod: the ripper. RoleEnv names the variable its
// Deployment sets to RoleRip, and no other pod carries it.
const (
	RoleEnv = "MEDIAOPS_ROLE"
	RoleRip = "rip"
)

// RequireRipRole refuses, with exit 3, unless this is the ripper pod. A run's
// liveness is a lease in one pod's /tmp, so a disc tool run anywhere else would
// read a live run as dead and write that verdict to the NAS. The check is
// positive, so a pod that never learned of the variable fails safe.
func RequireRipRole(tool string) error {
	if os.Getenv(RoleEnv) == RoleRip {
		return nil
	}
	return Refused(fmt.Errorf("the disc tools run only in the ripper pod (%s=%s); use kubectl -n mediaops exec deploy/mediaops-rip -- %s ARGS", RoleEnv, RoleRip, tool))
}
