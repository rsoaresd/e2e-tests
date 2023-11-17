package installation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"strings"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/devfile/library/v2/pkg/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"

	appclientset "github.com/argoproj/argo-cd/v2/pkg/client/clientset/versioned"
	kubeCl "github.com/redhat-appstudio/e2e-tests/pkg/clients/kubernetes"
	"github.com/redhat-appstudio/e2e-tests/pkg/constants"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	DEFAULT_TMP_DIR                     = "tmp"
	DEFAULT_INFRA_DEPLOYMENTS_BRANCH    = "main"
	DEFAULT_INFRA_DEPLOYMENTS_GH_ORG    = "redhat-appstudio"
	DEFAULT_LOCAL_FORK_NAME             = "qe"
	DEFAULT_LOCAL_FORK_ORGANIZATION     = "redhat-appstudio-qe"
	DEFAULT_E2E_APPLICATIONS_NAMEPSPACE = "appstudio-e2e-test"
	DEFAULT_E2E_QUAY_ORG                = "redhat-appstudio-qe"
)

var (
	previewInstallArgs = []string{"preview", "--keycloak", "--toolchain"}
)

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

type InstallAppStudio struct {
	// Kubernetes Client to interact with Openshift Cluster
	KubernetesClient *kubeCl.CustomClient

	// TmpDirectory to store temporary files like git repos or some metadata
	TmpDirectory string

	// Directory where to clone https://github.com/redhat-appstudio/infra-deployments repo
	InfraDeploymentsCloneDir string

	// Branch to clone from https://github.com/redhat-appstudio/infra-deployments. By default will be main
	InfraDeploymentsBranch string

	// Github organization from where will be cloned
	InfraDeploymentsOrganizationName string

	// Desired fork name for testing
	LocalForkName string

	// Github organization to use for testing purposes in preview mode
	LocalGithubForkOrganization string

	// Namespace where build applications will be placed
	E2EApplicationsNamespace string

	// base64-encoded content of a docker/config.json file which contains a valid login credentials for quay.io
	QuayToken string

	// Default quay organization for repositories generated by Image-controller
	DefaultImageQuayOrg string

	// Oauth2 token for default quay organization
	DefaultImageQuayOrgOAuth2Token string

	// Default expiration for image tags
	DefaultImageTagExpiration string
}

func NewAppStudioInstallController() (*InstallAppStudio, error) {
	cwd, _ := os.Getwd()
	k8sClient, err := kubeCl.NewAdminKubernetesClient()

	if err != nil {
		return nil, err
	}

	return &InstallAppStudio{
		KubernetesClient:                 k8sClient,
		TmpDirectory:                     DEFAULT_TMP_DIR,
		InfraDeploymentsCloneDir:         fmt.Sprintf("%s/%s/infra-deployments", cwd, DEFAULT_TMP_DIR),
		InfraDeploymentsBranch:           utils.GetEnv("INFRA_DEPLOYMENTS_BRANCH", DEFAULT_INFRA_DEPLOYMENTS_BRANCH),
		InfraDeploymentsOrganizationName: utils.GetEnv("INFRA_DEPLOYMENTS_ORG", DEFAULT_INFRA_DEPLOYMENTS_GH_ORG),
		LocalForkName:                    DEFAULT_LOCAL_FORK_NAME,
		LocalGithubForkOrganization:      utils.GetEnv("MY_GITHUB_ORG", DEFAULT_LOCAL_FORK_ORGANIZATION),
		QuayToken:                        utils.GetEnv("QUAY_TOKEN", ""),
		DefaultImageQuayOrg:              utils.GetEnv("DEFAULT_QUAY_ORG", ""),
		DefaultImageQuayOrgOAuth2Token:   utils.GetEnv("DEFAULT_QUAY_ORG_TOKEN", ""),
		DefaultImageTagExpiration:        utils.GetEnv(constants.IMAGE_TAG_EXPIRATION_ENV, constants.DefaultImageTagExpiration),
	}, nil
}

// Start the appstudio installation in preview mode.
func (i *InstallAppStudio) InstallAppStudioPreviewMode() error {
	if _, err := i.cloneInfraDeployments(); err != nil {
		return err
	}
	i.setInstallationEnvironments()

	if err := utils.ExecuteCommandInASpecificDirectory("hack/bootstrap-cluster.sh", previewInstallArgs, i.InfraDeploymentsCloneDir); err != nil {
		return err
	}

	i.addSPIOauthRedirectProxyUrl()

	return i.createE2EQuaySecret()
}

func (i *InstallAppStudio) setInstallationEnvironments() {
	os.Setenv("MY_GITHUB_ORG", i.LocalGithubForkOrganization)
	os.Setenv("MY_GITHUB_TOKEN", utils.GetEnv("GITHUB_TOKEN", ""))
	os.Setenv("MY_GIT_FORK_REMOTE", i.LocalForkName)
	os.Setenv("TEST_BRANCH_ID", util.GenerateRandomString(4))
	os.Setenv("QUAY_TOKEN", i.QuayToken)
	os.Setenv("IMAGE_CONTROLLER_QUAY_ORG", i.DefaultImageQuayOrg)
	os.Setenv("IMAGE_CONTROLLER_QUAY_TOKEN", i.DefaultImageQuayOrgOAuth2Token)
	os.Setenv("BUILD_SERVICE_IMAGE_TAG_EXPIRATION", i.DefaultImageTagExpiration)
	os.Setenv("PAC_GITHUB_APP_ID", utils.GetEnv("E2E_PAC_GITHUB_APP_ID", ""))                   // #nosec G104
	os.Setenv("PAC_GITHUB_APP_PRIVATE_KEY", utils.GetEnv("E2E_PAC_GITHUB_APP_PRIVATE_KEY", "")) // #nosec G104
	// setting 'vault' as default TOKENSTORAGE since SPI pretends to change the default value
	// more info: https://issues.redhat.com/browse/SVPI-683
	os.Setenv("IS_VAULT_TOKEN_STORAGE_TYPE", "true")
}

func (i *InstallAppStudio) cloneInfraDeployments() (*git.Remote, error) {
	dirInfo, err := os.Stat(i.InfraDeploymentsCloneDir)

	if !os.IsNotExist(err) && dirInfo.IsDir() {
		klog.Warningf("folder %s already exists... removing", i.InfraDeploymentsCloneDir)

		err := os.RemoveAll(i.InfraDeploymentsCloneDir)
		if err != nil {
			return nil, fmt.Errorf("error removing %s folder", i.InfraDeploymentsCloneDir)
		}
	}

	url := fmt.Sprintf("https://github.com/%s/infra-deployments", i.InfraDeploymentsOrganizationName)
	refName := fmt.Sprintf("refs/heads/%s", i.InfraDeploymentsBranch)
	klog.Infof("cloning '%s' with git ref '%s'", url, refName)
	repo, _ := git.PlainClone(i.InfraDeploymentsCloneDir, false, &git.CloneOptions{
		URL:           url,
		ReferenceName: plumbing.ReferenceName(refName),
		Progress:      os.Stdout,
	})

	return repo.CreateRemote(&config.RemoteConfig{Name: i.LocalForkName, URLs: []string{fmt.Sprintf("https://github.com/%s/infra-deployments.git", i.LocalGithubForkOrganization)}})
}

func (i *InstallAppStudio) CheckOperatorsReady() (err error) {
	apiConfig, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		klog.Fatal(err)
	}
	config, err := clientcmd.NewDefaultClientConfig(*apiConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	appClientset := appclientset.NewForConfigOrDie(config)

	patchPayload := []patchStringValue{{
		Op:    "replace",
		Path:  "/metadata/annotations/argocd.argoproj.io~1refresh",
		Value: "hard",
	}}
	patchPayloadBytes, err := json.Marshal(patchPayload)
	if err != nil {
		klog.Fatal(err)
	}
	_, err = appClientset.ArgoprojV1alpha1().Applications("openshift-gitops").Patch(context.Background(), "all-application-sets", types.JSONPatchType, patchPayloadBytes, metav1.PatchOptions{})
	if err != nil {
		klog.Fatal(err)
	}

	for {
		var count = 0
		appsListFor, err := appClientset.ArgoprojV1alpha1().Applications("openshift-gitops").List(context.Background(), metav1.ListOptions{})
		for _, app := range appsListFor.Items {
			fmt.Printf("Check application: %s\n", app.Name)
			application, err := appClientset.ArgoprojV1alpha1().Applications("openshift-gitops").Get(context.Background(), app.Name, metav1.GetOptions{})
			if err != nil {
				klog.Fatal(err)
			}

			if !(application.Status.Sync.Status == "Synced" && application.Status.Health.Status == "Healthy") {
				klog.Info("Application %s not ready\n", app.Name)
				count++
			} else if strings.Contains(application.String(), ("context deadline exceeded")) {
				fmt.Printf("Refreshing Application %s\n", app.Name)
				patchPayload := []patchStringValue{{
					Op:    "replace",
					Path:  "/metadata/annotations/argocd.argoproj.io~1refresh",
					Value: "soft",
				}}

				patchPayloadBytes, err := json.Marshal(patchPayload)
				if err != nil {
					klog.Fatal(err)
				}
				for _, app := range appsListFor.Items {
					_, err = i.KubernetesClient.KubeInterface().AppsV1().Deployments("openshift-gitops").Patch(context.Background(), app.Name, types.JSONPatchType, patchPayloadBytes, metav1.PatchOptions{})
					if err != nil {
						klog.Fatal(err)
					}
				}
			}
		}
		if err != nil {
			klog.Fatal(err)
		}

		if count == 0 {
			klog.Info("All Application are ready\n")
			break
		}
		time.Sleep(10 * time.Second)
	}
	return err
}

// Create secret in e2e-secrets which can be copied to testing namespaces
func (i *InstallAppStudio) createE2EQuaySecret() error {
	quayToken := os.Getenv("QUAY_TOKEN")
	if quayToken == "" {
		return fmt.Errorf("failed to obtain quay token from 'QUAY_TOKEN' env; make sure the env exists")
	}

	decodedToken, err := base64.StdEncoding.DecodeString(quayToken)
	if err != nil {
		return fmt.Errorf("failed to decode quay token. Make sure that QUAY_TOKEN env contain a base64 token")
	}

	namespace := constants.QuayRepositorySecretNamespace
	_, err = i.KubernetesClient.KubeInterface().CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			_, err := i.KubernetesClient.KubeInterface().CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("error when creating namespace %s : %v", namespace, err)
			}
		} else {
			return fmt.Errorf("error when getting namespace %s : %v", namespace, err)
		}
	}

	secretName := constants.QuayRepositorySecretName
	secret, err := i.KubernetesClient.KubeInterface().CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})

	if err != nil {
		if k8sErrors.IsNotFound(err) {
			_, err := i.KubernetesClient.KubeInterface().CoreV1().Secrets(namespace).Create(context.Background(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: decodedToken,
				},
			}, metav1.CreateOptions{})

			if err != nil {
				return fmt.Errorf("error when creating secret %s : %v", secretName, err)
			}
		} else {
			secret.Data = map[string][]byte{
				corev1.DockerConfigJsonKey: decodedToken,
			}
			_, err = i.KubernetesClient.KubeInterface().CoreV1().Secrets(namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("error when updating secret '%s' namespace: %v", secretName, err)
			}
		}
	}

	return nil
}

// Update spi-oauth-service-environment-config to add OAUTH_REDIRECT_PROXY_URL property for oauth tests
func (i *InstallAppStudio) addSPIOauthRedirectProxyUrl() {
	OauthRedirectProxyUrl := os.Getenv("OAUTH_REDIRECT_PROXY_URL")
	if OauthRedirectProxyUrl == "" {
		klog.Error("OAUTH_REDIRECT_PROXY_URL not set: not updating spi configuration")
		return
	}

	namespace := "spi-system"
	configMapName := "spi-oauth-service-environment-config"
	deploymentName := "spi-oauth-service"

	patchData := []byte(fmt.Sprintf(`{"data": {"OAUTH_REDIRECT_PROXY_URL": "%s"}}`, OauthRedirectProxyUrl))
	_, err := i.KubernetesClient.KubeInterface().CoreV1().ConfigMaps(namespace).Patch(context.Background(), configMapName, types.MergePatchType, patchData, metav1.PatchOptions{})
	if err != nil {
		klog.Error(err)
		return
	}

	namespacedName := types.NamespacedName{
		Name:      deploymentName,
		Namespace: namespace,
	}

	deployment := &appsv1.Deployment{}
	err = i.KubernetesClient.KubeRest().Get(context.Background(), namespacedName, deployment)
	if err != nil {
		klog.Error(err)
		return
	}

	newDeployment := deployment.DeepCopy()
	ann := newDeployment.ObjectMeta.Annotations
	if ann == nil {
		ann = make(map[string]string)
	}
	ann["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
	var replicas int32 = 0
	newDeployment.Spec.Replicas = &replicas
	newDeployment.SetAnnotations(ann)

	_, err = i.KubernetesClient.KubeInterface().AppsV1().Deployments(namespace).Update(context.Background(), newDeployment, metav1.UpdateOptions{})
	if err != nil {
		klog.Error(err)
		return
	}

}
