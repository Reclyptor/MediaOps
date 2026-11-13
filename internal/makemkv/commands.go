package makemkv

import "strconv"

// InfoArgv is the title-info pass over dev. --noscan keeps makemkvcon from
// probing every other drive on the host.
func InfoArgv(dev string, minLength int) []string {
	return []string{"makemkvcon", "-r", "--noscan", "--minlength=" + strconv.Itoa(minLength), "info", "dev:" + dev}
}

// RipArgv saves every title of at least minLength seconds from dev into dest.
func RipArgv(dev string, minLength int, dest string) []string {
	return []string{
		"makemkvcon", "-r", "--noscan", "--progress=-same",
		"--minlength=" + strconv.Itoa(minLength), "mkv", "dev:" + dev, "all", dest,
	}
}
