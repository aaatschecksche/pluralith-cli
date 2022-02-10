package strip

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"pluralith/pkg/auxiliary"
	"pluralith/pkg/ux"
	"reflect"
	"sort"
	"strings"
)

type StripState struct {
	keyBlacklist   []string
	keyDeletelist  []string
	valueBlacklist []string
	nameList       []string
	planJson       map[string]interface{}
}

// Helper function to produce hash digest of given string
func (S *StripState) Hash(value string) string {
	h := fnv.New64a()
	h.Write([]byte(value))
	return "hash_" + fmt.Sprintf("%v", h.Sum64())
}

// Helper function to build resource name list
func (S *StripState) BuildNameList(currentKey string, currentMap map[string]interface{}) {
	// Handle general value case
	if _, hasAddress := currentMap["address"]; hasAddress {
		if name, hasName := currentMap["name"]; hasName {
			S.nameList = append(S.nameList, name.(string))
		}
	}

	// Handle special key case for module names
	if currentKey == "module_calls" {
		for moduleKey, _ := range currentMap {
			S.nameList = append(S.nameList, moduleKey)
		}
	}

	// Handle special key case for variable names
	if currentKey == "variables" {
		for variableKey, _ := range currentMap {
			S.nameList = append(S.nameList, variableKey)
		}
	}
}

// Helper function to fetch provider names and exempt them from hashing
func (S *StripState) ExemptProviderNames(currentMap map[string]interface{}) {
	for _, providerObject := range currentMap {
		mapConversion := providerObject.(map[string]interface{})
		S.valueBlacklist = append(S.valueBlacklist, mapConversion["name"].(string))
	}
}

// Helper function to find and hash all resource names
func (S *StripState) ReplaceNames(value string) string {
	for _, name := range S.nameList {
		if strings.Contains(value, name) {
			nameHash := fmt.Sprintf("%v", S.Hash(name))
			value = strings.ReplaceAll(value, name, nameHash)
		}
	}

	return value
}

// Helper function to also hash any element in name list if it appears as key rather than value
func (S *StripState) HashNamesAsKeys(currentMap map[string]interface{}) {
	for mapKey, mapValue := range currentMap {
		for _, nameKey := range S.nameList {
			if mapKey == nameKey {
				currentMap[S.Hash(mapKey)] = mapValue
				delete(currentMap, mapKey)
			}
		}
	}
}

// Helper function to delete irrelevant keys
func (S *StripState) DeleteIrrelevantKeys(currentMap map[string]interface{}) {
	for _, irrelevantKey := range S.keyDeletelist {
		delete(currentMap, irrelevantKey)
	}
}

// Helper function to check if value needs to be blacklisted
func (S *StripState) CheckAndBlacklist(currentKey string, currentValue interface{}) {
	// If any of the keys in the blacklist are present -> add value to blacklist
	for _, blackKey := range S.keyBlacklist {
		if currentKey == blackKey {
			stringified := fmt.Sprintf("%v", currentValue)
			S.valueBlacklist = append(S.valueBlacklist, stringified)
		}
	}
}

// Helper function to hash values (differentiates between array values and object key values)
func (S *StripState) CheckAndHash(currentMap map[string]interface{}, currentKey string, index int) {
	var stringifiedValue string
	var blacklisted = false

	// Get value based on if array or not
	if index > -1 {
		slice := currentMap[currentKey].([]interface{})
		stringifiedValue = fmt.Sprintf("%v", slice[index])
	} else {
		stringifiedValue = fmt.Sprintf("%v", currentMap[currentKey])
	}

	// Check if blacklist contains value at current key if not a boolean
	for _, blackKey := range S.valueBlacklist {
		// Handle keys marked as prefixes (end with "*")
		if strings.HasSuffix(blackKey, "<value>") {
			noSuffixKey := strings.ReplaceAll(blackKey, "<value>", "")
			if strings.HasPrefix(stringifiedValue, noSuffixKey) {
				blacklisted = true
				break
			}
		}

		if stringifiedValue == blackKey {
			blacklisted = true
			break
		}
	}

	// Hash entire value if blacklisted
	if !blacklisted {
		// Set value based on if array or not
		if index > -1 {
			slice := currentMap[currentKey].([]interface{})
			slice[index] = S.Hash(stringifiedValue)
		} else {
			currentMap[currentKey] = S.Hash(stringifiedValue)
		}
	}

	// Replace resource names with their hashes in non-blacklisted values
	if blacklisted {
		if index > -1 {
			slice := currentMap[currentKey].([]interface{})
			slice[index] = S.ReplaceNames(stringifiedValue)
		} else {
			currentMap[currentKey] = S.ReplaceNames(stringifiedValue)
		}
	}
}

// Function to build a blacklist of values that should not be hashed
func (S *StripState) BuildBlacklist(planMap map[string]interface{}) {
	for key, value := range planMap {
		// Check if value at key is given
		if value != nil {
			outerValueType := reflect.TypeOf(value)

			// Get provider names to add to value blacklist
			if key == "provider_config" {
				S.ExemptProviderNames(value.(map[string]interface{}))
			}

			// Switch between different data types
			switch outerValueType.Kind() {
			case reflect.Map:
				currentMap := value.(map[string]interface{})
				// If value is of type map -> Move on to next recursion level
				S.BuildBlacklist(currentMap)
				S.BuildNameList(key, currentMap)
				S.DeleteIrrelevantKeys(currentMap)

			case reflect.Array, reflect.Slice:
				// If value is of type array or slice -> Loop through elements, if maps are found -> Move to next recursion level
				for _, sliceValue := range value.([]interface{}) {
					if reflect.TypeOf(sliceValue).Kind() == reflect.Map {
						currentMap := sliceValue.(map[string]interface{})

						S.BuildBlacklist(currentMap)
						S.BuildNameList(key, currentMap)
						S.DeleteIrrelevantKeys(currentMap)
					} else {
						S.CheckAndBlacklist(key, sliceValue)
					}
				}
			default:
				S.CheckAndBlacklist(key, value)
			}
		}
	}
}

// Function to process plan state and strip all sensitive data, keeping structure intact for debugging
func (S *StripState) ProcessState(currentMap map[string]interface{}) {
	for outerKey, outerValue := range currentMap {
		// Check if value at key is given
		if outerValue != nil {
			outerValueType := reflect.TypeOf(outerValue)

			// Switch between different data types
			switch outerValueType.Kind() {
			case reflect.Map:
				// If value is of type map -> Move on to next recursion level
				S.ProcessState(outerValue.(map[string]interface{}))
				S.HashNamesAsKeys(outerValue.(map[string]interface{}))
			case reflect.Array, reflect.Slice:
				// If value is of type array or slice -> Loop through elements, if maps are found -> Move to next recursion level
				for innerIndex, innerValue := range outerValue.([]interface{}) {
					if reflect.TypeOf(innerValue).Kind() == reflect.Map {
						S.ProcessState(innerValue.(map[string]interface{}))
						S.HashNamesAsKeys(innerValue.(map[string]interface{}))
					} else {
						S.CheckAndHash(currentMap, outerKey, innerIndex)
					}
				}
			default:
				S.CheckAndHash(currentMap, outerKey, -1)
			}
		}
	}
}

func (S *StripState) StripAndHash() error {
	functionName := "StripAndHashState"

	ux.PrintFormatted("⠿", []string{"blue"})
	ux.PrintFormatted(" Stripping Secrets", []string{"bold"})
	fmt.Println()
	fmt.Println()

	ux.PrintFormatted("→", []string{"blue"})
	fmt.Println(" We are stripping your plan state of secrets and hashing all values \n  to make it safe to share\n")

	stripSpinner := ux.NewSpinner("Stripping and hashing plan state", "Plan state stripped and hashed", "Stripping and hashing plan state failed", false)
	stripSpinner.Start()

	// Initialize various filter and hash lists
	S.keyBlacklist = []string{"address", "type", "module_address", "index", "provider_name"}
	S.keyDeletelist = []string{"tags", "tags_all", "description", "source"}
	S.valueBlacklist = []string{"each.key", "count.index", "module.<value>", "var.<value>"}

	// Initialize relevant paths
	planPath := filepath.Join(auxiliary.PathInstance.WorkingPath, "pluralith.state.stripped")
	outPath := filepath.Join(auxiliary.PathInstance.WorkingPath, "pluralith.state.hashed")

	// Check if plan state exists -> if not, alert and return
	if _, existErr := os.Stat(planPath); existErr != nil {
		stripSpinner.Fail("No Pluralith plan state found")
		ux.PrintFormatted("→ Run pluralith plan again\n\n", []string{"red"})

		return nil
	}

	// Read plan state
	planBytes, readErr := os.ReadFile(planPath)
	if readErr != nil {
		stripSpinner.Fail("Failed to read plan state")
		return fmt.Errorf("could not read plan state -> %v: %w", functionName, readErr)
	}

	// Parse plan state
	parseErr := json.Unmarshal(planBytes, &S.planJson)
	if parseErr != nil {
		stripSpinner.Fail("Failed to parse plan state")
		return fmt.Errorf("could not parse plan state -> %v: %w", functionName, readErr)
	}

	// Recursively collect exception values and build a blacklist
	S.BuildBlacklist(S.planJson)

	// Deduplicate value blacklist and name list
	S.valueBlacklist = auxiliary.DeduplicateSlice(S.valueBlacklist)
	S.nameList = auxiliary.DeduplicateSlice(S.nameList)

	// Sort name list by length to avoid erroneous substring matches
	sort.Slice(S.nameList, func(i, j int) bool {
		return len(S.nameList[i]) > len(S.nameList[j])
	})

	// Recursively process state
	S.ProcessState(S.planJson)

	// Turn stripped and hashed state back into string
	planString, marshalErr := json.MarshalIndent(S.planJson, "", " ")
	if marshalErr != nil {
		stripSpinner.Fail("Failed to stringify stripped plan state")
		return fmt.Errorf("%v: %w", functionName, marshalErr)
	}

	// Write stripped and hashed state to file
	if writeErr := os.WriteFile(outPath, planString, 0700); writeErr != nil {
		stripSpinner.Fail("Failed to write stripped plan state")
		return fmt.Errorf("%v: %w", functionName, writeErr)
	}

	stripSpinner.Success()
	ux.PrintFormatted("→", []string{"blue"})
	fmt.Print(" Inspect it in the ")
	ux.PrintFormatted("pluralith.state.hashed", []string{"blue"})
	fmt.Println(" file")
	fmt.Println()

	return nil
}

var StripInstance = &StripState{}