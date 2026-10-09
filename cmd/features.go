package cmd

import (
	"fmt"
	"strings"
)

// experimentalEnv switches the experimental commands on: `install` and `upgrade`.
// `demo` and `demo delete` are always available.
const experimentalEnv = "KUBRIX_EXPERIMENTAL"

// Features say which parts of the CLI are available.
type Features struct {
	// Experimental enables the commands that are not ready for everyone: install and upgrade.
	Experimental bool
}

// FeaturesFromEnv reads the features from the environment; KUBRIX_EXPERIMENTAL=1 (or true, yes, on) enables
// the experimental ones.
func FeaturesFromEnv(getenv func(string) string) Features {
	switch strings.ToLower(strings.TrimSpace(getenv(experimentalEnv))) {
	case "1", "true", "yes", "on":
		return Features{Experimental: true}
	}
	return Features{}
}

// requireExperimental is the error of an experimental command that was started without the feature.
func (f Features) requireExperimental(command string) error {
	if f.Experimental {
		return nil
	}
	return fmt.Errorf("%q is experimental; set %s=1 to use it", command, experimentalEnv)
}
