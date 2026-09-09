package utils

import (
	"io"
	"strings"

	"go.yaml.in/yaml/v4"
)

// ParseOSRelease parses an /etc/os-release file from the given reader and returns a map of key-value pairs.
func ParseOSRelease(reader io.Reader) (map[string]string, error) {
	result := make(map[string]string)
	data, err := io.ReadAll(reader)
	if err != nil {
		return result, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if len(strings.TrimSpace(line)) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		result[key] = value
	}
	return result, nil
}

// ExtensionCompatibility describes the compatibility of a Talos extension.
type ExtensionCompatibility struct {
	Version string `json:"version,omitempty"`
}

// ExtensionMetadata contains metadata about a Talos system extension.
type ExtensionMetadata struct {
	Name          string                            `json:"name,omitempty"`
	Version       string                            `json:"version,omitempty"`
	Author        string                            `json:"author,omitempty"`
	Description   string                            `json:"description,omitempty"`
	Compatibility map[string]ExtensionCompatibility `json:"compatibility,omitempty"`
	ExtraInfo     string                            `json:"extraInfo,omitempty"`
}

// ExtensionLayer represents a single layer in a Talos extension.
type ExtensionLayer struct {
	Image    string            `json:"image,omitempty"`
	Metadata ExtensionMetadata `json:"metadata,omitzero"`
}

// Extensions represents a collection of Talos system extension layers.
type Extensions struct {
	Layers []ExtensionLayer `json:"layers,omitempty"`
}

// GetSchematicID returns the schematic ID from the extensions, if present.
func (e *Extensions) GetSchematicID() string {
	lastLayer := e.Layers[len(e.Layers)-1]
	if lastLayer.Metadata.Name != "schematic" {
		return ""
	}
	return lastLayer.Metadata.Version
}

// ParseExtensions parses extensions from the given reader, presumably a file at /etc/extensions.yaml.
func ParseExtensions(reader io.Reader) (*Extensions, error) {
	ext := &Extensions{}

	data, err := io.ReadAll(reader)
	if err != nil {
		return ext, err
	}
	err = yaml.Unmarshal(data, ext)
	return ext, err
}
