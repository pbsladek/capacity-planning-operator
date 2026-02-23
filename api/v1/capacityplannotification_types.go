/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CapacityPlanNotificationSpec defines notification routing for a CapacityPlan digest.
type CapacityPlanNotificationSpec struct {
	// PlanRefName is the CapacityPlan name to monitor.
	PlanRefName string `json:"planRefName"`

	// Cooldown is the minimum time between notifications. Defaults to 30m.
	// +optional
	// +kubebuilder:default="30m"
	Cooldown metav1.Duration `json:"cooldown,omitempty"`

	// OnChangeOnly sends notifications only when risk snapshot hash changes.
	// Defaults to true.
	// +optional
	// +kubebuilder:default=true
	OnChangeOnly bool `json:"onChangeOnly,omitempty"`

	// DryRun computes and logs notifications without sending external traffic.
	// Defaults to false.
	// +optional
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`

	// Slack channel configuration.
	// +optional
	Slack SlackNotificationSpec `json:"slack,omitempty"`

	// Email channel configuration.
	// +optional
	Email EmailNotificationSpec `json:"email,omitempty"`
}

// SlackNotificationSpec configures outgoing Slack webhook notifications.
type SlackNotificationSpec struct {
	// Enabled toggles Slack delivery.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// WebhookSecretRefName is a Secret in the same namespace containing webhook URL.
	// +optional
	WebhookSecretRefName string `json:"webhookSecretRefName,omitempty"`

	// WebhookSecretKey is the key in WebhookSecretRefName. Defaults to webhookURL.
	// +optional
	// +kubebuilder:default="webhookURL"
	WebhookSecretKey string `json:"webhookSecretKey,omitempty"`
}

// EmailNotificationSpec configures outgoing SMTP notifications.
type EmailNotificationSpec struct {
	// Enabled toggles email delivery.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SMTPSecretRefName is a Secret in the same namespace containing SMTP credentials.
	// expected keys by default: host, port, username, password.
	// +optional
	SMTPSecretRefName string `json:"smtpSecretRefName,omitempty"`

	// SMTPHostKey defaults to "host".
	// +optional
	// +kubebuilder:default="host"
	SMTPHostKey string `json:"smtpHostKey,omitempty"`

	// SMTPPortKey defaults to "port".
	// +optional
	// +kubebuilder:default="port"
	SMTPPortKey string `json:"smtpPortKey,omitempty"`

	// SMTPUsernameKey defaults to "username".
	// +optional
	// +kubebuilder:default="username"
	SMTPUsernameKey string `json:"smtpUsernameKey,omitempty"`

	// SMTPPasswordKey defaults to "password".
	// +optional
	// +kubebuilder:default="password"
	SMTPPasswordKey string `json:"smtpPasswordKey,omitempty"`

	// From is the sender address.
	// +optional
	From string `json:"from,omitempty"`

	// To is the list of recipient addresses.
	// +optional
	To []string `json:"to,omitempty"`

	// SubjectPrefix is prepended to the email subject.
	// +optional
	SubjectPrefix string `json:"subjectPrefix,omitempty"`
}

// CapacityPlanNotificationStatus defines observed notification state.
type CapacityPlanNotificationStatus struct {
	// Conditions describe latest delivery state.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// LastSentTime is when a notification was last sent (or dry-run emitted).
	// +optional
	LastSentTime *metav1.Time `json:"lastSentTime,omitempty"`

	// LastDigestHash is the risk snapshot hash used for the last send.
	// +optional
	LastDigestHash string `json:"lastDigestHash,omitempty"`

	// LastMessage is a short summary of the last delivery operation.
	// +optional
	LastMessage string `json:"lastMessage,omitempty"`

	// LastSentChannels reports which channels were used for the last send.
	// +optional
	LastSentChannels []string `json:"lastSentChannels,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=capacityplannotifications,scope=Namespaced,shortName=cpn
// +kubebuilder:printcolumn:name="Plan",type=string,JSONPath=".spec.planRefName"
// +kubebuilder:printcolumn:name="Last Sent",type="date",JSONPath=".status.lastSentTime"

// CapacityPlanNotification routes CapacityPlan risk digests to external channels.
type CapacityPlanNotification struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CapacityPlanNotificationSpec   `json:"spec,omitempty"`
	Status CapacityPlanNotificationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CapacityPlanNotificationList contains a list of CapacityPlanNotification.
type CapacityPlanNotificationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapacityPlanNotification `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CapacityPlanNotification{}, &CapacityPlanNotificationList{})
}
