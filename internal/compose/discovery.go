package compose

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ServicesForImage returns services using one Compose image interpolation key.
// It reads the release's Compose document, so newly added workers sharing an
// image join scoped deploys without a CLI release.
func ServicesForImage(raw []byte, imageKey string) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse compose services: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose root must be a mapping")
	}
	services := mappingValue(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose services must be a mapping")
	}
	var out []string
	needle := "${" + imageKey
	for i := 0; i+1 < len(services.Content); i += 2 {
		image := mappingValue(services.Content[i+1], "image")
		if image != nil && (image.Value == needle+"}" ||
			len(image.Value) > len(needle) && image.Value[:len(needle)+1] == needle+":") {
			out = append(out, services.Content[i].Value)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no compose services use %s", imageKey)
	}
	return out, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
