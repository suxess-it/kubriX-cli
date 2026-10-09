package install

func caImportCommands(goos, path string) []string {
	switch goos {
	case "darwin":
		return []string{
			"sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + path,
			"(remove later: sudo security remove-trusted-cert -d " + path + ")",
		}
	case "windows":
		return []string{
			`certutil -addstore -f ROOT "` + path + `"   (elevated prompt)`,
		}
	default:
		return []string{
			"sudo cp " + path + " /usr/local/share/ca-certificates/kubrix-kind-ca.crt && sudo update-ca-certificates   (Debian/Ubuntu)",
			"certutil -d sql:$HOME/.pki/nssdb -A -t C,, -n kubrix-kind-ca -i " + path + "   (Chrome/Firefox, needs libnss3-tools)",
		}
	}
}
