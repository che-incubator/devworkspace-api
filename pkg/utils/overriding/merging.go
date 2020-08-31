package overriding

import (
	"fmt"
	"reflect"
	"strings"

	workspaces "github.com/devfile/api/pkg/apis/workspaces/v1alpha2"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func ensureNoConflictWithParent(mainContent *workspaces.DevWorkspaceTemplateSpecContent, parentflattenedContent *workspaces.DevWorkspaceTemplateSpecContent) error {
	return checkKeys(func(elementType string, keysSets []sets.String) []error {
		mainKeys := keysSets[0]
		parentOrPluginKeys := keysSets[1]
		overriddenElementsInMainContent := mainKeys.Intersection(parentOrPluginKeys)
		if overriddenElementsInMainContent.Len() > 0 {
			return []error{fmt.Errorf("Some %s are already defined in parent: %s. "+
				"If you want to override them, you should do it in the parent scope.",
				elementType,
				strings.Join(overriddenElementsInMainContent.List(), ", "))}
		}
		return []error{}
	},
		mainContent, parentflattenedContent)
}

func ensureNoConflictsWithPlugins(mainContent *workspaces.DevWorkspaceTemplateSpecContent, pluginFlattenedContents ...*workspaces.DevWorkspaceTemplateSpecContent) error {
	getPluginKey := func(pluginIndex int) string {
		index := 0
		for _, comp := range mainContent.Components {
			if comp.Plugin != nil {
				if pluginIndex == index {
					return comp.Name
				}
				index++
			}
		}
		return "unknown"
	}

	allSpecs := []workspaces.TopLevelListContainer{mainContent}
	for _, plugipluginFlattenedContent := range pluginFlattenedContents {
		allSpecs = append(allSpecs, plugipluginFlattenedContent)
	}
	return checkKeys(func(elementType string, keysSets []sets.String) []error {
		mainKeys := keysSets[0]
		pluginKeysSets := keysSets[1:]
		errs := []error{}
		for pluginNumber, pluginKeys := range pluginKeysSets {
			overriddenElementsInMainContent := mainKeys.Intersection(pluginKeys)

			if overriddenElementsInMainContent.Len() > 0 {
				errs = append(errs, fmt.Errorf("Some %s are already defined in plugin '%s': %s. "+
					"If you want to override them, you should do it in the plugin scope.",
					elementType,
					getPluginKey(pluginNumber),
					strings.Join(overriddenElementsInMainContent.List(), ", ")))
			}
		}
		return errs
	},
		allSpecs...)
}

// MergeDevWorkspaceTemplateSpec implements the merging logic of a main devfile content with flattened, already-overriden parent devfiles or plugins.
// On an `main` `DevfileWorkspaceTemplateSpec` (which is the core part of a devfile, without the `apiVersion` and `metadata`),
// it allows adding all the new overriden elements provided by flattened parent and plugins
//
// It is not allowed for to have duplicate (== with same key) commands, components or projects between the main content and the parent or plugins.
// An error would be thrown
//
// The result is a transformed `DevfileWorkspaceTemplateSpec` object, that does not contain any `plugin` component
// (since they are expected to be provided as flattened overriden devfiles in teh arguments)
func MergeDevWorkspaceTemplateSpec(
	mainContent *workspaces.DevWorkspaceTemplateSpecContent,
	parentFlattenedContent *workspaces.DevWorkspaceTemplateSpecContent,
	pluginFlattenedContents ...*workspaces.DevWorkspaceTemplateSpecContent) (*workspaces.DevWorkspaceTemplateSpecContent, error) {

	allContents := []*workspaces.DevWorkspaceTemplateSpecContent{parentFlattenedContent}
	allContents = append(allContents, pluginFlattenedContents...)
	allContents = append(allContents, mainContent)

	if err := ensureNoConflictWithParent(mainContent, parentFlattenedContent); err != nil {
		return nil, err
	}

	if err := ensureNoConflictsWithPlugins(mainContent, pluginFlattenedContents...); err != nil {
		return nil, err
	}

	result := workspaces.DevWorkspaceTemplateSpecContent{}

	preStartCommands := sets.String{}
	postStartCommands := sets.String{}
	preStopCommands := sets.String{}
	postStopCommands := sets.String{}

	topLevelListsNames := result.GetToplevelLists()
	resultValue := reflect.ValueOf(&result).Elem()
	componentType := reflect.TypeOf(workspaces.Component{})
	for toplevelListName := range topLevelListsNames {
		listType, fieldExists := resultValue.Type().FieldByName(toplevelListName)
		if !fieldExists {
			return nil, fmt.Errorf("field '%v' is unknown in %v struct (this should never happen)", toplevelListName, reflect.TypeOf(resultValue).Name())
		}
		if listType.Type.Kind() != reflect.Slice {
			return nil, fmt.Errorf("field '%v' in %v struct is not a slice (this should never happen)", toplevelListName, reflect.TypeOf(resultValue).Name())
		}
		listElementType := listType.Type.Elem()
		resultToplevelListValue := resultValue.FieldByName(toplevelListName)
		for _, content := range allContents {
			contentValue := reflect.ValueOf(content).Elem()
			contentToplevelListValue := contentValue.FieldByName(toplevelListName)
			if !contentToplevelListValue.IsNil() {
				for i := 0; i < contentToplevelListValue.Len(); i++ {
					elementValue := contentToplevelListValue.Index(i)
					// We skip the plugins of the main content, since they have been provided by flattened plugin content.
					if content == mainContent && listElementType.ConvertibleTo(componentType) {
						component := elementValue.Interface().(workspaces.Component)
						if component.Plugin != nil {
							continue
						}
					}
					if resultToplevelListValue.IsNil() {
						resultToplevelListValue.Set(reflect.MakeSlice(reflect.SliceOf(listElementType), 0, contentToplevelListValue.Len()))
					}
					resultToplevelListValue.Set(reflect.Append(resultToplevelListValue, elementValue))
				}
			}
		}
	}

	for _, content := range allContents {
		if content.Events != nil {
			if result.Events == nil {
				result.Events = &workspaces.Events{}
			}
			preStartCommands = preStartCommands.Union(sets.NewString(content.Events.PreStart...))
			postStartCommands = postStartCommands.Union(sets.NewString(content.Events.PostStart...))
			preStopCommands = preStopCommands.Union(sets.NewString(content.Events.PreStop...))
			postStopCommands = postStopCommands.Union(sets.NewString(content.Events.PostStop...))
		}
	}

	if result.Events != nil {
		result.Events.PreStart = preStartCommands.List()
		result.Events.PostStart = postStartCommands.List()
		result.Events.PreStop = preStopCommands.List()
		result.Events.PostStop = postStopCommands.List()
	}

	return &result, nil
}

// MergeDevWorkspaceTemplateSpecBytes implements the merging logic of a main devfile content with flattened, already-overridden parent devfiles or plugins.
// On an json or yaml document that contains the core content of the devfile (which is the core part of a devfile, without the `apiVersion` and `metadata`),
// it allows adding all the new overridden elements provided by flattened parent and plugins (also provided as json or yaml documents)
//
// It is not allowed for to have duplicate (== with same key) commands, components or projects between the main content and the parent or plugins.
// An error would be thrown
//
// The result is a transformed `DevfileWorkspaceTemplateSpec` object, that does not contain any `plugin` component
// (since they are expected to be provided as flattened overridden devfiles in the arguments)
func MergeDevWorkspaceTemplateSpecBytes(originalBytes []byte, flattenedParentBytes []byte, flattenPluginsBytes ...[]byte) (*workspaces.DevWorkspaceTemplateSpecContent, error) {
	originalJson, err := yaml.ToJSON(originalBytes)
	if err != nil {
		return nil, err
	}

	original := workspaces.DevWorkspaceTemplateSpecContent{}
	err = json.Unmarshal(originalJson, &original)
	if err != nil {
		return nil, err
	}

	flattenedParentJson, err := yaml.ToJSON(flattenedParentBytes)
	if err != nil {
		return nil, err
	}

	flattenedParent := workspaces.DevWorkspaceTemplateSpecContent{}
	err = json.Unmarshal(flattenedParentJson, &flattenedParent)
	if err != nil {
		return nil, err
	}

	flattenedPlugins := []*workspaces.DevWorkspaceTemplateSpecContent{}
	for _, flattenedPluginBytes := range flattenPluginsBytes {
		flattenedPluginJson, err := yaml.ToJSON(flattenedPluginBytes)
		if err != nil {
			return nil, err
		}

		flattenedPlugin := workspaces.DevWorkspaceTemplateSpecContent{}
		err = json.Unmarshal(flattenedPluginJson, &flattenedPlugin)
		if err != nil {
			return nil, err
		}
		flattenedPlugins = append(flattenedPlugins, &flattenedPlugin)
	}

	return MergeDevWorkspaceTemplateSpec(&original, &flattenedParent, flattenedPlugins...)
}
