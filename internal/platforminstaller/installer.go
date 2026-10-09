// Package platforminstaller runs the kubriX platform installer, a Kubernetes Job from the kubriX source repository,
// and reads what it is installed from.
package platforminstaller

import (
	"bytes"
	"fmt"
	"regexp"
)

const (
	ImageRepo  = "ghcr.io/suxess-it/kubrix-installer"
	Namespace  = "kubrix-install"
	JobName    = "kubrix-install-job"
	SecretName = "kubrix-install-secrets"
)

var invalidTagChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// BranchTag mirrors docker/metadata-action's `type=ref,event=branch` tag naming.
func BranchTag(branch string) string {
	return invalidTagChars.ReplaceAllString(branch, "-")
}

// SetImageTag points the manifest's installer image at tag.
func SetImageTag(manifest []byte, tag string) ([]byte, error) {
	latest := []byte("image: " + ImageRepo + ":latest")
	if !bytes.Contains(manifest, latest) {
		return nil, fmt.Errorf("install-manifests.yaml does not reference %s:latest", ImageRepo)
	}
	return bytes.ReplaceAll(manifest, latest, []byte("image: "+ImageRepo+":"+tag)), nil
}
