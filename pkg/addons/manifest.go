/*
Copyright 2020 The KubeOne Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package addons

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	"github.com/kubermatic/kubeone/pkg/state"

	metav1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func getManifests(s *state.State) error {
	vars := map[string]interface{}{
		"KubeOne": s.Cluster,
	}
	manifests, err := loadAddonsManifests(s.Cluster.Addons.Path, s.Verbose, vars)
	if err != nil {
		return err
	}

	rawManifests, err := ensureAddonsLabelsOnResources(manifests)
	if err != nil {
		return err
	}

	combinedManifests := combineManifests(rawManifests)
	s.Configuration.AddFile("addons/addons.yaml", combinedManifests.String())

	return nil
}

// loadAddonsManifests loads all YAML files from a given directory and runs the templating logic
func loadAddonsManifests(addonsPath string, verbose bool, vars map[string]interface{}) ([]runtime.RawExtension, error) {
	manifests := []runtime.RawExtension{}

	files, err := ioutil.ReadDir(addonsPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read the addons directory %s", addonsPath)
	}

	for _, file := range files {
		filePath := filepath.Join(addonsPath, file.Name())
		if file.IsDir() {
			fmt.Printf("Found directory '%s' in the addons path. Ignoring.\n", file.Name())
			continue
		}
		if verbose {
			fmt.Printf("Parsing addons manifest '%s'\n", file.Name())
		}

		manifestBytes, err := ioutil.ReadFile(filePath)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to load addon %s", file.Name())
		}

		tpl, err := template.New("addons-base").Parse(string(manifestBytes))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to template addons manifest %s", file.Name())
		}
		buf := bytes.NewBuffer([]byte{})
		if err := tpl.Execute(buf, vars); err != nil {
			return nil, errors.Wrapf(err, "failed to template addons manifest %s", file.Name())
		}

		trim := strings.TrimSpace(buf.String())
		if len(trim) == 0 {
			fmt.Printf("Addons manifest '%s' is empty after parsing. Skipping.\n", file.Name())
		}

		reader := kyaml.NewYAMLReader(bufio.NewReader(buf))
		for {
			b, err := reader.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, errors.Wrapf(err, "failed reading from YAML reader for manifest %s", file.Name())
			}
			b = bytes.TrimSpace(b)
			if len(b) == 0 {
				continue
			}
			decoder := kyaml.NewYAMLToJSONDecoder(bytes.NewBuffer(b))
			raw := runtime.RawExtension{}
			if err := decoder.Decode(&raw); err != nil {
				return nil, errors.Wrapf(err, "failed to decode manifest %s", file.Name())
			}
			if len(raw.Raw) == 0 {
				// This can happen if the manifest contains only comments
				continue
			}
			manifests = append(manifests, raw)
		}
	}

	return manifests, nil
}

// ensureAddonsLabelsOnResources applies the addons label on all resources in the manifest
func ensureAddonsLabelsOnResources(manifests []runtime.RawExtension) ([]*bytes.Buffer, error) {
	var rawManifests []*bytes.Buffer

	for _, m := range manifests {
		parsedUnstructuredObj := &metav1unstructured.Unstructured{}
		if _, _, err := metav1unstructured.UnstructuredJSONScheme.Decode(m.Raw, nil, parsedUnstructuredObj); err != nil {
			return nil, errors.Wrapf(err, "failed to parse unstructured fields")
		}

		existingLabels := parsedUnstructuredObj.GetLabels()
		if existingLabels == nil {
			existingLabels = map[string]string{}
		}
		existingLabels[addonLabel] = ""
		parsedUnstructuredObj.SetLabels(existingLabels)

		jsonBuffer := &bytes.Buffer{}
		if err := metav1unstructured.UnstructuredJSONScheme.Encode(parsedUnstructuredObj, jsonBuffer); err != nil {
			return nil, fmt.Errorf("encoding json failed: %v", err)
		}

		// Must be encoded back to YAML, otherwise kubectl fails to apply because it tries to parse the whole
		// thing as json
		yamlBytes, err := yaml.JSONToYAML(jsonBuffer.Bytes())
		if err != nil {
			return nil, err
		}

		rawManifests = append(rawManifests, bytes.NewBuffer(yamlBytes))
	}

	return rawManifests, nil
}

// combineManifests combines all manifest into a single one.
// This is needed so we can properly utilize kubectl apply --prune
func combineManifests(manifests []*bytes.Buffer) *bytes.Buffer {
	parts := make([]string, len(manifests))
	for i, m := range manifests {
		s := m.String()
		s = strings.TrimSuffix(s, "\n")
		s = strings.TrimSpace(s)
		parts[i] = s
	}

	return bytes.NewBufferString(strings.Join(parts, "\n---\n") + "\n")
}
