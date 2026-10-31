package fossillib

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// GetSetting returns one repository setting. It runs "fossil settings NAME
// --value", so versionable settings are resolved the same way the rest of
// Fossil resolves them.
func (r *LocalFossilRepository) GetSetting(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty setting name")
	}
	out, err := r.run("settings", name, "--value")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// SetSetting changes one repository setting.
func (r *LocalFossilRepository) SetSetting(name, value string) error {
	if name == "" {
		return fmt.Errorf("empty setting name")
	}
	_, err := r.run("settings", name, value)
	return err
}

// UnsetSetting clears one repository setting.
func (r *LocalFossilRepository) UnsetSetting(name string) error {
	if name == "" {
		return fmt.Errorf("empty setting name")
	}
	_, err := r.run("unset", name)
	return err
}

// GetAllSettings lists the repository settings. Values can contain spaces;
// Fossil pads the setting name to a fixed column, which is used to split.
func (r *LocalFossilRepository) GetAllSettings() (map[string]string, error) {
	out, err := r.run("settings")
	if err != nil {
		return nil, err
	}
	res := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		idx := strings.IndexFunc(line, unicode.IsSpace)
		if idx < 0 {
			res[line] = ""
			continue
		}
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx:])
		if name == "" {
			continue
		}
		res[name] = value
	}
	return res, nil
}

// GetConfigArea returns one area of Fossil's configuration (project, skin,
// ticket or all) as a string map.
func (r *LocalFossilRepository) GetConfigArea(area string) (map[string]string, error) {
	if area == "" {
		area = "project"
	}
	payload, err := r.runJSON("config", "get", area)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return map[string]string{}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("%w: fossil json config get: %v", ErrInvalidFormat, err)
	}
	res := make(map[string]string, len(raw))
	for k, v := range raw {
		switch value := v.(type) {
		case nil:
			res[k] = ""
		case string:
			res[k] = value
		case bool:
			res[k] = fmt.Sprint(value)
		case float64:
			res[k] = fmt.Sprint(value)
		default:
			b, err := json.Marshal(value)
			if err != nil {
				res[k] = fmt.Sprint(value)
			} else {
				res[k] = string(b)
			}
		}
	}
	return res, nil
}
