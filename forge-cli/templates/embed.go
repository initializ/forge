// Package templates provides embedded template files for forge.
package templates

import "embed"

//go:embed Dockerfile.tmpl deployment.yaml.tmpl service.yaml.tmpl network-policy.yaml.tmpl secrets.yaml.tmpl docker-compose.yaml.tmpl init wrapper
var FS embed.FS

// GetInitTemplate reads a template file from the init directory.
func GetInitTemplate(path string) (string, error) {
	data, err := FS.ReadFile("init/" + path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetWrapperTemplate reads a template file from the wrapper directory.
func GetWrapperTemplate(name string) (string, error) {
	data, err := FS.ReadFile("wrapper/" + name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
