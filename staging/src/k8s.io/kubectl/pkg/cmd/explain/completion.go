/*
Copyright 2026 The Kubernetes Authors.

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

package explain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	openapiclient "k8s.io/client-go/openapi"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/utils/ptr"
	"k8s.io/kubectl/pkg/cmd/apiresources"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/explain"
)

// resourceFieldCompletionFunc returns a completion function for kubectl explain that completes:
// - resource types when no dot is present (e.g., "pods", "deploy")
// - field paths when a dot is present (e.g., "pods.spec", "pods.spec.containers")
func resourceFieldCompletionFunc(f cmdutil.Factory) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		dotIdx := strings.Index(toComplete, ".")
		if dotIdx == -1 {
			// No dot: complete resource type names. Use all resources (no verb filter) because
			// kubectl explain works for any resource, not just those that support GET.
			comps := explainResourceList(f, cmd, toComplete)
			for i, c := range comps {
				comps[i] = c + "."
			}
			return comps, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		}

		// There is a dot: determine where the resource part ends and fields begin.
		// The resource name is always the first segment (before the first dot).
		resourcePart := toComplete[:dotIdx]

		mapper, err := f.ToRESTMapper()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		gvrs, err := mapper.ResourcesFor(schema.GroupVersionResource{Resource: resourcePart})
		if err != nil || len(gvrs) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		// Determine where the resource identifier ends and the field path begins.
		// A resource identifier may include the group (e.g. "poddisruptionbudgets.policy").
		// Three cases for each GVR whose groupResource string is relevant:
		//   1. toComplete starts with groupResource+"."  → field path follows the group suffix
		//   2. toComplete == groupResource               → resource fully typed, show all fields
		//   3. groupResource starts with toComplete (not ending with ".") → still completing the resource name
		var selectedGVR schema.GroupVersionResource
		var baseBase string // "resource." or "resource.group." prefix for completions
		var fieldPart string
		var resourceNameComps []string

		for _, g := range gvrs {
			groupResource := g.GroupResource().String()
			switch {
			case strings.HasPrefix(toComplete, groupResource+"."):
				selectedGVR = g
				baseBase = groupResource + "."
				fieldPart = toComplete[len(groupResource)+1:]
			case toComplete == groupResource:
				selectedGVR = g
				baseBase = groupResource + "."
				fieldPart = ""
			case strings.HasPrefix(groupResource, toComplete) && !strings.HasSuffix(toComplete, "."):
				// Only suggest group-qualified names when the user is mid-word (no trailing dot).
				// A trailing dot means the user finished the resource name and wants field completions.
				resourceNameComps = append(resourceNameComps, groupResource+".")
			}
			if !selectedGVR.Empty() {
				break
			}
		}

		if selectedGVR.Empty() {
			if len(resourceNameComps) > 0 {
				// User is still typing the group suffix (e.g. "poddisruptionbudgets.pol").
				// Return the full resource identifiers with a trailing dot so the next
				// tab press moves straight into field completion.
				return resourceNameComps, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
			}
			// Fallback for resources used without an explicit group (e.g. "deployments.spec"
			// or "deployments." where the user wants field completion without specifying the group).
			selectedGVR = gvrs[0]
			baseBase = resourcePart + "."
			fieldPart = toComplete[dotIdx+1:]
		}

		// Split the field part into the path to navigate and the prefix to filter by.
		// Guard lastDot > 0 to avoid splitting on a leading dot (e.g. double-dot input).
		var fieldsPath []string
		prefix := fieldPart
		basePrefix := baseBase

		if lastDot := strings.LastIndex(fieldPart, "."); lastDot > 0 {
			fieldsPath = strings.Split(fieldPart[:lastDot], ".")
			prefix = fieldPart[lastDot+1:]
			basePrefix = baseBase + fieldPart[:lastDot+1]
		}

		// Mirror renderOpenAPIV2: ignore the error from KindFor, only fall back when gvk is empty.
		gvk, _ := mapper.KindFor(selectedGVR)
		if gvk.Empty() {
			gvk, err = mapper.KindFor(selectedGVR.GroupResource().WithVersion(""))
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}

		expandable, leaves := fieldNamesForGVK(f, selectedGVR, gvk, fieldsPath)

		var comps []string
		hasExpandable := false
		for _, name := range expandable {
			if strings.HasPrefix(name, prefix) {
				comps = append(comps, basePrefix+name+".")
				hasExpandable = true
			}
		}
		for _, name := range leaves {
			if strings.HasPrefix(name, prefix) {
				comps = append(comps, basePrefix+name)
			}
		}
		// Only suppress the trailing space when there are expandable (dot-ending) completions.
		// For leaf-only results the shell should insert a space after the completed field name.
		directive := cobra.ShellCompDirectiveNoFileComp
		if hasExpandable {
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		return comps, directive
	}
}

// explainResourceList returns all API resource type names matching toComplete, with no verb filter.
// kubectl explain can describe any resource regardless of which verbs it supports.
func explainResourceList(f cmdutil.Factory, cmd *cobra.Command, toComplete string) []string {
	buf := new(bytes.Buffer)
	streams := genericiooptions.IOStreams{In: os.Stdin, Out: buf, ErrOut: io.Discard}
	o := apiresources.NewAPIResourceOptions(streams)
	o.PrintFlags.OutputFormat = ptr.To("name")
	o.Cached = true
	if err := o.Complete(f, cmd, nil); err != nil {
		return nil
	}
	_ = o.RunAPIResources()
	var comps []string
	for _, res := range strings.Split(buf.String(), "\n") {
		if res != "" && strings.HasPrefix(res, toComplete) {
			comps = append(comps, res)
		}
	}
	return comps
}

// fieldNamesForGVK returns the expandable and leaf field names at the given path within
// the schema for gvk. It tries OpenAPI v3 first (so CRD fields are complete) and falls back to v2.
func fieldNamesForGVK(f cmdutil.Factory, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, fieldsPath []string) (expandable, leaves []string) {
	if v3Client, err := f.OpenAPIV3Client(); err == nil {
		if exp, lv, err := v3FieldNames(v3Client, gvr, gvk, fieldsPath); err == nil {
			return exp, lv
		}
	}

	// v2 fallback
	openAPIResources, err := f.OpenAPISchema()
	if err != nil {
		return nil, nil
	}
	s := openAPIResources.LookupResource(gvk)
	if s == nil {
		return nil, nil
	}
	s, err = explain.LookupSchemaForField(s, fieldsPath)
	if err != nil {
		return nil, nil
	}
	v := &fieldNameVisitor{}
	s.Accept(v)
	return v.expandable, v.leaves
}

// v3FieldNames enumerates field names at fieldsPath within the v3 OpenAPI schema for gvr/gvk.
func v3FieldNames(client openapiclient.Client, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, fieldsPath []string) (expandable, leaves []string, err error) {
	paths, err := client.Paths()
	if err != nil {
		return nil, nil, err
	}

	var resourcePath string
	if len(gvr.Group) == 0 {
		resourcePath = fmt.Sprintf("api/%s", gvr.Version)
	} else {
		resourcePath = fmt.Sprintf("apis/%s/%s", gvr.Group, gvr.Version)
	}

	gvPath, exists := paths[resourcePath]
	if !exists {
		return nil, nil, fmt.Errorf("no v3 path for %s", resourcePath)
	}

	schemaBytes, err := gvPath.Schema("application/json")
	if err != nil {
		return nil, nil, err
	}

	var document map[string]interface{}
	if err := json.Unmarshal(schemaBytes, &document); err != nil {
		return nil, nil, err
	}

	// Find the schema whose x-kubernetes-group-version-kind matches gvk.
	components, _ := document["components"].(map[string]interface{})
	schemas, _ := components["schemas"].(map[string]interface{})

	var resourceSchema map[string]interface{}
	for _, schemaAny := range schemas {
		schemaMap, ok := schemaAny.(map[string]interface{})
		if !ok {
			continue
		}
		gvkList, ok := schemaMap["x-kubernetes-group-version-kind"].([]interface{})
		if !ok {
			continue
		}
		for _, entry := range gvkList {
			e, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			if e["group"] == gvk.Group && e["version"] == gvk.Version && e["kind"] == gvk.Kind {
				resourceSchema = schemaMap
				break
			}
		}
		if resourceSchema != nil {
			break
		}
	}
	if resourceSchema == nil {
		return nil, nil, fmt.Errorf("no v3 schema for %v", gvk)
	}

	// Navigate to the requested field path.
	current := resourceSchema
	for _, field := range fieldsPath {
		current = v3ResolveSchema(current, document)
		// Step into array items before looking up the next field name
		// (e.g. navigating through spec.containers requires entering the array's items).
		if items, ok := current["items"].(map[string]interface{}); ok {
			current = v3ResolveSchema(items, document)
		}
		props, _ := current["properties"].(map[string]interface{})
		if props == nil {
			return nil, nil, fmt.Errorf("no properties at field %q", field)
		}
		next, ok := props[field].(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("field %q not found", field)
		}
		current = next
	}

	current = v3ResolveSchema(current, document)
	// When the target field is an array, enumerate the element type's properties
	// (e.g. pods.spec.containers. should show Container fields, not array-level keys).
	if items, ok := current["items"].(map[string]interface{}); ok {
		current = v3ResolveSchema(items, document)
	}
	props, _ := current["properties"].(map[string]interface{})
	for name, fieldAny := range props {
		field, ok := fieldAny.(map[string]interface{})
		if !ok {
			leaves = append(leaves, name)
			continue
		}
		if v3IsExpandable(field, document) {
			expandable = append(expandable, name)
		} else {
			leaves = append(leaves, name)
		}
	}
	return expandable, leaves, nil
}

// v3ResolveSchema resolves $ref and unwraps a single-element allOf with no sibling properties.
func v3ResolveSchema(s map[string]interface{}, document map[string]interface{}) map[string]interface{} {
	if ref, ok := s["$ref"].(string); ok {
		if resolved := v3ResolveRef(ref, document); resolved != nil {
			return v3ResolveSchema(resolved, document)
		}
	}
	if allOf, ok := s["allOf"].([]interface{}); ok && len(allOf) == 1 {
		if _, hasProps := s["properties"]; !hasProps {
			if item, ok := allOf[0].(map[string]interface{}); ok {
				return v3ResolveSchema(item, document)
			}
		}
	}
	return s
}

// v3ResolveRef follows a JSON Reference of the form "#/components/schemas/...".
func v3ResolveRef(ref string, document map[string]interface{}) map[string]interface{} {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var current interface{} = document
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = m[part]
	}
	result, _ := current.(map[string]interface{})
	return result
}

// v3IsExpandable reports whether a v3 schema node has navigable sub-fields.
func v3IsExpandable(s map[string]interface{}, document map[string]interface{}) bool {
	resolved := v3ResolveSchema(s, document)
	if _, ok := resolved["properties"]; ok {
		return true
	}
	if items, ok := resolved["items"].(map[string]interface{}); ok {
		itemResolved := v3ResolveSchema(items, document)
		if _, ok := itemResolved["properties"]; ok {
			return true
		}
	}
	return false
}

// fieldNameVisitor collects field names from a proto.Schema, split into
// expandable fields (objects/arrays-of-objects that can be drilled into with a
// trailing dot) and leaf fields (primitives, maps, etc.).
type fieldNameVisitor struct {
	expandable []string
	leaves     []string
}

func (v *fieldNameVisitor) VisitKind(k *proto.Kind) {
	for _, name := range k.Keys() {
		ev := &fieldExpandabilityVisitor{}
		k.Fields[name].Accept(ev)
		if ev.expandable {
			v.expandable = append(v.expandable, name)
		} else {
			v.leaves = append(v.leaves, name)
		}
	}
}
func (v *fieldNameVisitor) VisitArray(a *proto.Array)         { a.SubType.Accept(v) }
func (v *fieldNameVisitor) VisitMap(_ *proto.Map)             {}
func (v *fieldNameVisitor) VisitPrimitive(_ *proto.Primitive) {}
func (v *fieldNameVisitor) VisitReference(r proto.Reference)  { r.SubSchema().Accept(v) }
func (v *fieldNameVisitor) VisitArbitrary(_ *proto.Arbitrary) {}

// fieldExpandabilityVisitor reports whether a schema node has navigable sub-fields.
type fieldExpandabilityVisitor struct {
	expandable bool
}

func (v *fieldExpandabilityVisitor) VisitKind(_ *proto.Kind)           { v.expandable = true }
func (v *fieldExpandabilityVisitor) VisitArray(a *proto.Array)         { a.SubType.Accept(v) }
func (v *fieldExpandabilityVisitor) VisitMap(_ *proto.Map)             {}
func (v *fieldExpandabilityVisitor) VisitPrimitive(_ *proto.Primitive) {}
func (v *fieldExpandabilityVisitor) VisitReference(r proto.Reference)  { r.SubSchema().Accept(v) }
func (v *fieldExpandabilityVisitor) VisitArbitrary(_ *proto.Arbitrary) {}
