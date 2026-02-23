/*
Copyright 2024 pbsladek.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SampleSpec defines the desired state of Sample.
type SampleSpec struct {
	// Foo is an example field of Sample.
	// Edit sample_types.go to remove/update.
	// +optional
	Foo string `json:"foo,omitempty"`

	// Size defines the number of desired replicas.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	Size int32 `json:"size,omitempty"`
}

// SampleStatus defines the observed state of Sample.
type SampleStatus struct {
	// Conditions store the status conditions of the Sample instances.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=samples,scope=Namespaced
// +kubebuilder:printcolumn:name="Foo",type=string,JSONPath=`.spec.foo`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Sample is the Schema for the samples API.
type Sample struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SampleSpec   `json:"spec,omitempty"`
	Status SampleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SampleList contains a list of Sample.
type SampleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Sample `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Sample{}, &SampleList{})
}
