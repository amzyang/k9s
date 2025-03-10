// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/config/data"
	"github.com/derailed/k9s/internal/config/json"
	"github.com/derailed/k9s/internal/slogs"
	"gopkg.in/yaml.v2"
)

// ViewConfigListener represents a view config listener.
type ViewConfigListener interface {
	// ViewSettingsChanged notifies listener the view configuration changed.
	ViewSettingsChanged(*ViewSetting)

	// GetNamespace return the view namespace
	GetNamespace() string
}

// ViewSetting represents a view configuration.
type ViewSetting struct {
	Columns    []string `yaml:"columns"`
	SortColumn string   `yaml:"sortColumn"`
}

func (v *ViewSetting) HasCols() bool {
	return len(v.Columns) > 0
}

func (v *ViewSetting) IsBlank() bool {
	return v == nil || (len(v.Columns) == 0 && v.SortColumn == "")
}

func (v *ViewSetting) SortCol() (string, bool, error) {
	if v == nil || v.SortColumn == "" {
		return "", false, fmt.Errorf("no sort column specified")
	}
	tt := strings.Split(v.SortColumn, ":")
	if len(tt) < 2 {
		return "", false, fmt.Errorf("invalid sort column spec: %q. must be col-name:asc|desc", v.SortColumn)
	}

	return tt[0], tt[1] == "asc", nil
}

// Equals checks if two view settings are equal.
func (v *ViewSetting) Equals(vs *ViewSetting) bool {
	if v == nil && vs == nil {
		return true
	}
	if v == nil || vs == nil {
		return false
	}

	if c := slices.Compare(v.Columns, vs.Columns); c != 0 {
		return false
	}

	return cmp.Compare(v.SortColumn, vs.SortColumn) == 0
}

// CustomView represents a collection of view customization.
type CustomView struct {
	Views     map[string]ViewSetting `yaml:"views"`
	listeners map[string]ViewConfigListener
}

// NewCustomView returns a views configuration.
func NewCustomView() *CustomView {
	return &CustomView{
		Views:     make(map[string]ViewSetting),
		listeners: make(map[string]ViewConfigListener),
	}
}

// Reset clears out configurations.
func (v *CustomView) Reset() {
	for k := range v.Views {
		delete(v.Views, k)
	}
}

// Load loads view configurations.
func (v *CustomView) Load(path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	bb, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := data.JSONValidator.Validate(json.ViewsSchema, bb); err != nil {
		slog.Warn("Validation failed. Please update your config and restart!",
			slogs.Path, path,
			slogs.Error, err,
		)
	}
	var in CustomView
	if err := yaml.Unmarshal(bb, &in); err != nil {
		return err
	}
	v.Views = in.Views
	v.fireConfigChanged()

	return nil
}

// AddListener registers a new listener.
func (v *CustomView) AddListener(gvr string, l ViewConfigListener) {
	v.listeners[gvr] = l
	v.fireConfigChanged()
}

// RemoveListener unregister a listener.
func (v *CustomView) RemoveListener(gvr string) {
	delete(v.listeners, gvr)
}

func (v *CustomView) fireConfigChanged() {
	for gvr, list := range v.listeners {
		if vs := v.getVS(gvr, list.GetNamespace()); vs == nil {
			list.ViewSettingsChanged(nil)
		} else {
			slog.Debug("Reloading custom view settings", slogs.GVR, gvr)
			list.ViewSettingsChanged(vs)
		}
	}
}

func (v *CustomView) getVS(gvr, ns string) *ViewSetting {
	k := gvr
	if ns != "" {
		k += "@" + ns
	}

	for key := range maps.Keys(v.Views) {
		if !strings.HasPrefix(key, gvr) {
			continue
		}

		switch {
		case key == k:
			vs := v.Views[key]
			return &vs
		case strings.Contains(key, "@"):
			tt := strings.Split(key, "@")
			if len(tt) != 2 {
				break
			}
			if rx, err := regexp.Compile(tt[1]); err == nil && rx.MatchString(k) {
				vs := v.Views[key]
				return &vs
			}
		}
	}

	return nil
}
