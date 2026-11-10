package test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type helmMetadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type helmSubject struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type helmRule struct {
	APIGroups []string `yaml:"apiGroups"`
	Resources []string `yaml:"resources"`
	Verbs     []string `yaml:"verbs"`
}

type helmContainer struct {
	Name    string   `yaml:"name"`
	Image   string   `yaml:"image"`
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
	} `yaml:"env"`
	EnvFrom []struct {
		SecretRef struct {
			Name string `yaml:"name"`
		} `yaml:"secretRef"`
	} `yaml:"envFrom"`
	SecurityContext struct {
		RunAsNonRoot             bool `yaml:"runAsNonRoot"`
		RunAsUser                int  `yaml:"runAsUser"`
		RunAsGroup               int  `yaml:"runAsGroup"`
		AllowPrivilegeEscalation bool `yaml:"allowPrivilegeEscalation"`
	} `yaml:"securityContext"`
	ReadinessProbe helmProbe `yaml:"readinessProbe"`
	LivenessProbe  helmProbe `yaml:"livenessProbe"`
}

type helmProbe struct {
	HTTPGet struct {
		Path string `yaml:"path"`
		Port string `yaml:"port"`
	} `yaml:"httpGet"`
}

type helmManifest struct {
	Kind     string        `yaml:"kind"`
	Metadata helmMetadata  `yaml:"metadata"`
	Subjects []helmSubject `yaml:"subjects"`
	Rules    []helmRule    `yaml:"rules"`
	RoleRef  struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	} `yaml:"roleRef"`
	Spec struct {
		Template struct {
			Spec struct {
				ServiceAccountName string `yaml:"serviceAccountName"`
				SecurityContext    struct {
					FSGroup int `yaml:"fsGroup"`
				} `yaml:"securityContext"`
				Containers []helmContainer `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func renderHelm(t *testing.T, namespace string, arguments ...string) ([]byte, error) {
	t.Helper()

	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for chart rendering tests")
	}

	const timeout = 30 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := append([]string{"template", "cloudian", "../helm/cosi-driver", "--namespace", namespace}, arguments...)

	output, err := exec.CommandContext(ctx, helm, args...).CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("render chart: %w", err)
	}

	return output, nil
}

func TestHelmRequiresV1alpha2SidecarImage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		arguments []string
		wantError string
	}{
		{name: "repository", wantError: "sidecar.image.repository must reference a COSI v1alpha2 sidecar image"},
		{
			name: "tag", arguments: []string{"--set", "sidecar.image.repository=example.invalid/cosi-sidecar"},
			wantError: "sidecar.image.tag must identify a COSI v1alpha2 build",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := renderHelm(t, "cosi-system", tc.arguments...)
			require.Error(t, err)
			require.Contains(t, string(output), tc.wantError)
		})
	}
}

func TestHelmV1alpha2Deployment(t *testing.T) {
	t.Parallel()

	for _, namespace := range []string{"default", "cosi-system"} {
		t.Run(namespace, func(t *testing.T) {
			t.Parallel()

			output, err := renderHelm(t, namespace,
				"--set", "sidecar.image.repository=example.invalid/cosi-sidecar",
				"--set-string", "sidecar.image.tag=274540c69400",
			)
			require.NoError(t, err, "%s", output)
			manifests := decodeHelm(t, output)
			require.Len(t, manifests, 7)

			roleName := namespace + "-cloudian-cosi-provisioner"

			for _, manifest := range manifests {
				switch manifest.Kind {
				case "Deployment":
					assertHelmDeployment(t, namespace, manifest)
				case "ServiceAccount", "Secret":
					require.Equal(t, namespace, manifest.Metadata.Namespace)
				case "Role":
					require.Equal(t, namespace, manifest.Metadata.Namespace)
					require.Equal(t, []helmRule{{
						APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "create", "update", "patch"},
					}}, manifest.Rules)
				case "ClusterRoleBinding", "RoleBinding":
					require.Equal(t, []helmSubject{{Kind: "ServiceAccount", Name: "objectstorage-provisioner-sa", Namespace: namespace}}, manifest.Subjects)

					if manifest.Kind == "ClusterRoleBinding" {
						require.Equal(t, roleName, manifest.RoleRef.Name)
					}
				case "ClusterRole":
					require.Equal(t, roleName, manifest.Metadata.Name)
					require.Contains(t, manifest.Rules, helmRule{
						APIGroups: []string{"objectstorage.k8s.io"}, Resources: []string{"buckets", "bucketaccesses"},
						Verbs: []string{"get", "list", "watch", "update", "patch"},
					})
					require.Contains(t, manifest.Rules, helmRule{
						APIGroups: []string{"objectstorage.k8s.io"}, Resources: []string{"buckets/status", "bucketaccesses/status"}, Verbs: []string{"get", "update", "patch"},
					})
					require.Contains(t, manifest.Rules, helmRule{
						APIGroups: []string{"objectstorage.k8s.io"}, Resources: []string{"buckets/finalizers", "bucketaccesses/finalizers"}, Verbs: []string{"update"},
					})
					require.Contains(t, manifest.Rules, helmRule{
						APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"},
					})

					for _, rule := range manifest.Rules {
						require.NotContains(t, rule.Resources, "leases")
						require.NotContains(t, rule.Resources, "bucketclaims")
						require.NotContains(t, rule.Resources, "bucketaccessclasses")
					}
				}
			}
		})
	}
}

func assertHelmDeployment(t *testing.T, namespace string, manifest helmManifest) {
	t.Helper()

	require.Equal(t, namespace, manifest.Metadata.Namespace)
	pod := manifest.Spec.Template.Spec
	require.Equal(t, "objectstorage-provisioner-sa", pod.ServiceAccountName)
	require.Equal(t, 65532, pod.SecurityContext.FSGroup)
	require.Len(t, pod.Containers, 2)
	driver, sidecar := pod.Containers[0], pod.Containers[1]
	require.Equal(t, []string{"/bin/sh", "-c"}, driver.Command)
	require.Equal(t, []string{"umask 0007; exec /entrypoint.sh"}, driver.Args)

	for _, env := range driver.Env {
		require.NotEqual(t, "ADMIN_ENDPOINT", env.Name)
	}

	require.Equal(t, "example.invalid/cosi-sidecar:274540c69400", sidecar.Image)
	require.Contains(t, sidecar.Args, "--leader-elect=true")
	require.NotContains(t, sidecar.Args, "--debug")
	require.NotContains(t, sidecar.Args, "-v")
	require.True(t, sidecar.SecurityContext.RunAsNonRoot)
	require.Equal(t, 65532, sidecar.SecurityContext.RunAsUser)
	require.Equal(t, 65532, sidecar.SecurityContext.RunAsGroup)
	require.False(t, sidecar.SecurityContext.AllowPrivilegeEscalation)
	require.Equal(t, "/readyz", sidecar.ReadinessProbe.HTTPGet.Path)
	require.Equal(t, "/healthz", sidecar.LivenessProbe.HTTPGet.Path)
	require.Equal(t, "health", sidecar.ReadinessProbe.HTTPGet.Port)
	require.Equal(t, "health", sidecar.LivenessProbe.HTTPGet.Port)
	require.Len(t, sidecar.Env, 1)
	require.Equal(t, "COSI_ENDPOINT", sidecar.Env[0].Name)
	require.Equal(t, "unix:///var/lib/cosi/cosi.sock", sidecar.Env[0].Value)
}

func decodeHelm(t *testing.T, output []byte) []helmManifest {
	t.Helper()

	var manifests []helmManifest

	decoder := yaml.NewDecoder(bytes.NewReader(output))

	for {
		var manifest helmManifest

		err := decoder.Decode(&manifest)
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)

		manifests = append(manifests, manifest)
	}

	return manifests
}

func TestHelmExistingCredentialsAndAdmin(t *testing.T) {
	t.Parallel()

	output, err := renderHelm(t, "cosi-system",
		"--set", "sidecar.image.repository=example.invalid/cosi-sidecar",
		"--set-string", "sidecar.image.tag=274540c69400",
		"--set", "existingCredentialsSecret=existing-credentials",
		"--set", "admin.endpoint=https://admin.example.invalid",
	)
	require.NoError(t, err, "%s", output)
	manifests := decodeHelm(t, output)
	require.Len(t, manifests, 6)

	for _, manifest := range manifests {
		require.NotEqual(t, "Secret", manifest.Kind)

		if manifest.Kind == "Deployment" {
			driver := manifest.Spec.Template.Spec.Containers[0]
			require.Len(t, driver.EnvFrom, 1)
			require.Equal(t, "existing-credentials", driver.EnvFrom[0].SecretRef.Name)

			foundAdmin := false

			for _, env := range driver.Env {
				if env.Name == "ADMIN_ENDPOINT" {
					foundAdmin = true

					require.Equal(t, "https://admin.example.invalid", env.Value)
				}
			}

			require.True(t, foundAdmin)
		}
	}
}
