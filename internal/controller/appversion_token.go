/*
Copyright 2026 Mohan Raj.

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

package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

const (
	// tokenVolume is the name of the volume with the seed of the tenant token key in an App pod.
	tokenVolume = "koodam-tenant-token"
	// tokenDir has the seed in the check container.
	tokenDir = "/etc/koodam-tenant-token"
	// tokenSeedField is the key of the seed in the Secret.
	tokenSeedField = "seed"
	// tokenKeyIDAnnotation has an ID of the public key in the pod. A new key changes the pod
	// template, so the Deployment makes new pods with the new seed. The proxy does not read it.
	tokenKeyIDAnnotation = "koodam.dev/tenant-token-key-id"
	// msgTokenName is the message for a template that uses a name of the tenant token.
	msgTokenName = "the name is reserved for the tenant token key"
)

// errTokenSecret tells that the Secret with the name of the tenant token key is not from the
// version, or that it has no valid seed.
var errTokenSecret = errors.New("the Secret of the tenant token key is not from the version")

// tokenEnabled tells if the check container signs a tenant token. The check has a tenant label
// only with a tenant claim.
func (c AppClaimCheck) tokenEnabled() bool {
	return c.enabled() && c.TenantClaim != "" && c.TenantToken
}

func tenantTokenName(av *corev1alpha1.AppVersion) string { return appVersionName(av) + "-tenant-token" }

// tokenKeyID gives back an ID of a public key text. It is not a secret.
func tokenKeyID(public string) string {
	h := sha256.Sum256([]byte(public))
	return hex.EncodeToString(h[:8])
}

// checkTokenNames refuses a template that can give the seed to a container of the user: a
// volume or a volume mount with the name of the seed volume. The pod template rules already
// refuse each reference to a Secret with the prefix of the version objects, so no volume or
// variable of the user can name the Secret of the seed. Only the check container can read it.
func checkTokenNames(tmpl *corev1.PodTemplateSpec) error {
	path := field.NewPath("spec", "podTemplate", "spec")
	var errs field.ErrorList
	for _, v := range tmpl.Spec.Volumes {
		if v.Name == tokenVolume {
			errs = append(errs, field.Invalid(path.Child("volumes"), v.Name, msgTokenName))
		}
	}
	for _, ct := range slices.Concat(tmpl.Spec.Containers, tmpl.Spec.InitContainers) {
		if slices.ContainsFunc(ct.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == tokenVolume }) {
			errs = append(errs, field.Invalid(path.Child("containers"), ct.Name, msgTokenName))
		}
	}
	if len(errs) > 0 {
		return &invalidTemplateError{errs: errs}
	}
	return nil
}

// tenantTokenKey reads the Secret with the seed of the tenant token key of the version. If there
// is no Secret, it makes one with a new seed. It gives back the public key as text. It gives
// back errTokenSecret for a Secret that is not from the version or that has no valid seed. The
// seed is only in the Secret: no log, event, status or error has it.
func (r *AppVersionReconciler) tenantTokenKey(ctx context.Context, av *corev1alpha1.AppVersion) (string, error) {
	m := appVersionMeta(av)
	m.Name = tenantTokenName(av)
	s := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Name}, s)
	switch {
	case apierrors.IsNotFound(err):
		seed := make([]byte, tenanttoken.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return "", err
		}
		s = &corev1.Secret{ObjectMeta: m, Immutable: new(true), Data: map[string][]byte{tokenSeedField: seed}}
		if err := r.Create(ctx, s); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !metav1.IsControlledBy(s, av):
		return "", errTokenSecret
	}
	key, err := tenanttoken.PrivateKey(s.Data[tokenSeedField])
	if err != nil {
		return "", errTokenSecret
	}
	return tenanttoken.PublicText(key), nil
}

// addToken gives the seed to the check container and sets its flag. Only the check container
// mounts the volume of the seed.
func addToken(tmpl *corev1.PodTemplateSpec, check *corev1.Container, av *corev1alpha1.AppVersion, public string) {
	metav1.SetMetaDataAnnotation(&tmpl.ObjectMeta, tokenKeyIDAnnotation, tokenKeyID(public))
	tmpl.Spec.Volumes = append(tmpl.Spec.Volumes, corev1.Volume{Name: tokenVolume, VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: tenantTokenName(av), DefaultMode: new(int32(0o440)),
			Items: []corev1.KeyToPath{{Key: tokenSeedField, Path: tokenSeedField}}}}})
	check.Args = append(check.Args, "--tenant-token-key-file", tokenDir+"/"+tokenSeedField)
	check.VolumeMounts = append(check.VolumeMounts, corev1.VolumeMount{Name: tokenVolume, MountPath: tokenDir, ReadOnly: true})
}
