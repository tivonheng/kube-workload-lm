package policy

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

func Decode(data []byte) (PolicySet, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return PolicySet{}, fmt.Errorf("policy document is empty")
	}
	var document yaml.Node
	if err := decodeOne(data, &document, false); err != nil {
		return PolicySet{}, err
	}
	if err := rejectNulls(&document, "$"); err != nil {
		return PolicySet{}, err
	}
	var raw rawPolicySet
	if err := decodeOne(data, &raw, true); err != nil {
		return PolicySet{}, err
	}
	return compilePolicySet(raw)
}

func decodeOne(data []byte, target any, strict bool) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(strict)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode policy: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode policy: multiple YAML documents are not allowed")
		}
		return fmt.Errorf("decode trailing policy document: %w", err)
	}
	return nil
}

func rejectNulls(node *yaml.Node, path string) error {
	if node.Tag == "!!null" {
		return fmt.Errorf("explicit null is not allowed at %s", path)
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := rejectNulls(child, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if err := rejectNulls(value, path+"."+key.Value); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			if err := rejectNulls(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return rejectNulls(node.Alias, path)
	}
	return nil
}
