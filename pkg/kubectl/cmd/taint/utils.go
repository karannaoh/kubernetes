/*
Copyright 2018 The Kubernetes Authors.

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

// Package taints implements utilites for working with taints
package taint

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Exported taint constant strings
const (
	MODIFIED  = "modified"
	TAINTED   = "tainted"
	UNTAINTED = "untainted"
)

// parseTaints takes a spec which is an array and creates slices for new taints to be added, taints to be deleted.
func parseTaints(spec []string) ([]corev1.Taint, []corev1.Taint, error) {
	var taints, taintsToRemove []corev1.Taint
	uniqueTaints := map[corev1.TaintEffect]sets.String{}

	for _, taintSpec := range spec {
		if strings.Index(taintSpec, "=") != -1 && strings.Index(taintSpec, ":") != -1 {
			newTaint, err := parseTaint(taintSpec)
			if err != nil {
				return nil, nil, err
			}
			// validate if taint is unique by <key, effect>
			if len(uniqueTaints[newTaint.Effect]) > 0 && uniqueTaints[newTaint.Effect].Has(newTaint.Key) {
				return nil, nil, fmt.Errorf("duplicated taints with the same key and effect: %v", newTaint)
			}
			// add taint to existingTaints for uniqueness check
			if len(uniqueTaints[newTaint.Effect]) == 0 {
				uniqueTaints[newTaint.Effect] = sets.String{}
			}
			uniqueTaints[newTaint.Effect].Insert(newTaint.Key)

			taints = append(taints, newTaint)
		} else if strings.HasSuffix(taintSpec, "-") {
			taintKey := taintSpec[:len(taintSpec)-1]
			var effect corev1.TaintEffect
			if strings.Index(taintKey, ":") != -1 {
				parts := strings.Split(taintKey, ":")
				taintKey = parts[0]
				effect = corev1.TaintEffect(parts[1])
			}

			// If effect is specified, need to validate it.
			if len(effect) > 0 {
				err := validateTaintEffect(effect)
				if err != nil {
					return nil, nil, err
				}
			}
			taintsToRemove = append(taintsToRemove, corev1.Taint{Key: taintKey, Effect: effect})
		} else {
			return nil, nil, fmt.Errorf("unknown taint spec: %v", taintSpec)
		}
	}
	return taints, taintsToRemove, nil
}

// parseTaint parses a taint from a string. Taint must be of the format '<key>=<value>:<effect>'.
func parseTaint(st string) (corev1.Taint, error) {
	var taint corev1.Taint
	parts := strings.Split(st, "=")
	if len(parts) != 2 || len(parts[1]) == 0 || len(validation.IsQualifiedName(parts[0])) > 0 {
		return taint, fmt.Errorf("invalid taint spec: %v", st)
	}

	parts2 := strings.Split(parts[1], ":")

	errs := validation.IsValidLabelValue(parts2[0])
	if len(parts2) != 2 || len(errs) != 0 {
		return taint, fmt.Errorf("invalid taint spec: %v, %s", st, strings.Join(errs, "; "))
	}

	effect := corev1.TaintEffect(parts2[1])
	if err := validateTaintEffect(effect); err != nil {
		return taint, err
	}

	taint.Key = parts[0]
	taint.Value = parts2[0]
	taint.Effect = effect

	return taint, nil
}

func validateTaintEffect(effect corev1.TaintEffect) error {
	if effect != corev1.TaintEffectNoSchedule && effect != corev1.TaintEffectPreferNoSchedule && effect != corev1.TaintEffectNoExecute {
		return fmt.Errorf("invalid taint effect: %v, unsupported taint effect", effect)
	}

	return nil
}

// ReorganizeTaints returns the updated set of taints, taking into account old taints that were not updated,
// old taints that were updated, old taints that were deleted, and new taints.
func reorganizeTaints(node *corev1.Node, overwrite bool, taintsToAdd []corev1.Taint, taintsToRemove []corev1.Taint) (string, []corev1.Taint, error) {
	newTaints := append([]corev1.Taint{}, taintsToAdd...)
	oldTaints := node.Spec.Taints
	// add taints that already existing but not updated to newTaints
	added := addTaints(oldTaints, &newTaints)
	allErrs, deleted := deleteTaints(taintsToRemove, &newTaints)
	if (added && deleted) || overwrite {
		return MODIFIED, newTaints, utilerrors.NewAggregate(allErrs)
	} else if added {
		return TAINTED, newTaints, utilerrors.NewAggregate(allErrs)
	}
	return UNTAINTED, newTaints, utilerrors.NewAggregate(allErrs)
}

// deleteTaints deletes the given taints from the node's taintlist.
func deleteTaints(taintsToRemove []corev1.Taint, newTaints *[]corev1.Taint) ([]error, bool) {
	allErrs := []error{}
	var removed bool
	for _, taintToRemove := range taintsToRemove {
		removed = false
		if len(taintToRemove.Effect) > 0 {
			*newTaints, removed = deleteTaint(*newTaints, &taintToRemove)
		} else {
			*newTaints, removed = deleteTaintsByKey(*newTaints, taintToRemove.Key)
		}
		if !removed {
			allErrs = append(allErrs, fmt.Errorf("taint %q not found", taintToRemove.ToString()))
		}
	}
	return allErrs, removed
}

// addTaints adds the newTaints list to existing ones and updates the newTaints List.
// TODO: This needs a rewrite to take only the new values instead of appended newTaints list to be consistent.
func addTaints(oldTaints []corev1.Taint, newTaints *[]corev1.Taint) bool {
	for _, oldTaint := range oldTaints {
		existsInNew := false
		for _, taint := range *newTaints {
			if taint.MatchTaint(&oldTaint) {
				existsInNew = true
				break
			}
		}
		if !existsInNew {
			*newTaints = append(*newTaints, oldTaint)
		}
	}
	return len(oldTaints) != len(*newTaints)
}

// CheckIfTaintsAlreadyExists checks if the node already has taints that we want to add and returns a string with taint keys.
func checkIfTaintsAlreadyExists(oldTaints []corev1.Taint, taints []corev1.Taint) string {
	var existingTaintList = make([]string, 0)
	for _, taint := range taints {
		for _, oldTaint := range oldTaints {
			if taint.Key == oldTaint.Key && taint.Effect == oldTaint.Effect {
				existingTaintList = append(existingTaintList, taint.Key)
			}
		}
	}
	return strings.Join(existingTaintList, ",")
}

// DeleteTaintsByKey removes all the taints that have the same key to given taintKey
func deleteTaintsByKey(taints []corev1.Taint, taintKey string) ([]corev1.Taint, bool) {
	newTaints := []corev1.Taint{}
	deleted := false
	for i := range taints {
		if taintKey == taints[i].Key {
			deleted = true
			continue
		}
		newTaints = append(newTaints, taints[i])
	}
	return newTaints, deleted
}

// DeleteTaint removes all the taints that have the same key and effect to given taintToDelete.
func deleteTaint(taints []corev1.Taint, taintToDelete *corev1.Taint) ([]corev1.Taint, bool) {
	newTaints := []corev1.Taint{}
	deleted := false
	for i := range taints {
		if taintToDelete.MatchTaint(&taints[i]) {
			deleted = true
			continue
		}
		newTaints = append(newTaints, taints[i])
	}
	return newTaints, deleted
}
