package spi

import (
	"fmt"
	"time"

	"github.com/devfile/library/v2/pkg/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redhat-appstudio/e2e-tests/pkg/framework"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils"
	"github.com/redhat-appstudio/remote-secret/api/v1beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

/*
 * Component: spi
 * Description: SVPI-558 - Test all the options of the authz of remote secret target deployment
 */

var _ = framework.SPISuiteDescribe(Label("spi-suite", "remote-secret"), func() {

	defer GinkgoRecover()

	Describe("SVPI-558 - Using service account to authenticate and deploy to the target namespaces", Ordered, func() {
		var fw *framework.Framework
		var err error
		var namespace string
		var remoteSecret *v1beta1.RemoteSecret

		remoteSecretName := fmt.Sprintf("test-remote-secret-%s", util.GenerateRandomString(4))
		targetNamespace1 := fmt.Sprintf("spi-test-target1-%s", util.GenerateRandomString(4))
		targetNamespace2 := fmt.Sprintf("spi-test-target2-%s", util.GenerateRandomString(4))
		targetSecretName1 := ""
		targetSecretName2 := ""
		serviceAccountName := fmt.Sprintf("deployment-enabler-%s", util.GenerateRandomString(4))
		roleName := fmt.Sprintf("deployment-enabler-%s", util.GenerateRandomString(4))
		roleBindingName := fmt.Sprintf("deployment-enabler-%s", util.GenerateRandomString(4))

		BeforeAll(func() {
			// Initialize the tests controllers
			fw, err = framework.NewFramework(utils.GetGeneratedNamespace("spi-demos"))
			Expect(err).NotTo(HaveOccurred())
			namespace = fw.UserNamespace
			Expect(namespace).NotTo(BeEmpty())

			_, err = fw.AsKubeAdmin.CommonController.CreateTestNamespace(targetNamespace1)
			Expect(err).NotTo(HaveOccurred(), "Error when creating %s namespace: %v", targetNamespace1, err)

			_, err = fw.AsKubeAdmin.CommonController.CreateTestNamespace(targetNamespace2)
			Expect(err).NotTo(HaveOccurred(), "Error when creating %s namespace: %v", targetNamespace2, err)
		})

		AfterAll(func() {
			if !CurrentSpecReport().Failed() {
				Expect(fw.SandboxController.DeleteUserSignup(fw.UserName)).To(BeTrue())
				Expect(fw.AsKubeAdmin.CommonController.DeleteNamespace(targetNamespace1)).To(Succeed())
				Expect(fw.AsKubeAdmin.CommonController.DeleteNamespace(targetNamespace2)).To(Succeed())
			}
		})

		It("creates RemoteSecret with previously created namespaces as targets", func() {
			remoteSecret, err = fw.AsKubeDeveloper.SPIController.CreateRemoteSecret(remoteSecretName, namespace, []string{targetNamespace1, targetNamespace2})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				remoteSecret, err = fw.AsKubeDeveloper.SPIController.GetRemoteSecret(remoteSecretName, namespace)
				Expect(err).NotTo(HaveOccurred())

				return meta.IsStatusConditionFalse(remoteSecret.Status.Conditions, "DataObtained")
			}, 5*time.Minute, 5*time.Second).Should(BeTrue(), fmt.Sprintf("RemoteSecret %s/%s is not waiting for data", namespace, remoteSecretName))
		})

		It("creates service account", func() {
			labels := map[string]string{"appstudio.redhat.com/remotesecret-auth-sa": "true"}
			_, err := fw.AsKubeAdmin.CommonController.CreateServiceAccount(serviceAccountName, namespace, nil, labels)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates role for target 1", func() {
			_, err := fw.AsKubeAdmin.CommonController.CreateRole(roleName, targetNamespace1, map[string][]string{
				"apiGroupsList": {""},
				"roleResources": {"secrets", "serviceaccounts"},
				"roleVerbs":     {"create", "get", "list", "update", "delete"},
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = fw.AsKubeAdmin.CommonController.GetRole(roleName, targetNamespace1)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates role for target 2", func() {
			_, err := fw.AsKubeAdmin.CommonController.CreateRole(roleName, targetNamespace2, map[string][]string{
				"apiGroupsList": {""},
				"roleResources": {"secrets", "serviceaccounts"},
				"roleVerbs":     {"create", "get", "list", "update", "delete"},
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = fw.AsKubeAdmin.CommonController.GetRole(roleName, targetNamespace2)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates role binding for target 1", func() {
			_, err := fw.AsKubeAdmin.CommonController.CreateRoleBinding(
				roleBindingName, targetNamespace1,
				"ServiceAccount", serviceAccountName, namespace,
				"Role", roleName, "rbac.authorization.k8s.io",
			)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates role binding for target 2", func() {
			_, err := fw.AsKubeAdmin.CommonController.CreateRoleBinding(
				roleBindingName, targetNamespace2,
				"ServiceAccount", serviceAccountName, namespace,
				"Role", roleName, "rbac.authorization.k8s.io",
			)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates upload secret", func() {
			s := &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-secret",
					Labels: map[string]string{
						"appstudio.redhat.com/upload-secret": "remotesecret",
					},
					Annotations: map[string]string{
						"appstudio.redhat.com/remotesecret-name": remoteSecret.Name,
					},
				},
				Type: v1.SecretTypeOpaque,
				StringData: map[string]string{
					"a": "b",
					"c": "d",
				},
			}

			_, err = fw.AsKubeAdmin.CommonController.CreateSecret(namespace, s)
			Expect(err).NotTo(HaveOccurred())
		})

		It("checks if remote secret was deployed", func() {
			Eventually(func() bool {
				remoteSecret, err = fw.AsKubeDeveloper.SPIController.GetRemoteSecret(remoteSecretName, namespace)
				Expect(err).NotTo(HaveOccurred())

				return meta.IsStatusConditionTrue(remoteSecret.Status.Conditions, "Deployed")
			}, 5*time.Minute, 5*time.Second).Should(BeTrue(), fmt.Sprintf("RemoteSecret %s/%s is not in deployed phase", namespace, remoteSecretName))
		})

		It("checks targets in RemoteSecret status", func() {
			remoteSecret, err = fw.AsKubeDeveloper.SPIController.GetRemoteSecret(remoteSecret.Name, namespace)
			Expect(err).NotTo(HaveOccurred())

			targets := remoteSecret.Status.Targets
			Expect(targets).To(HaveLen(2))

			// get targetSecretName1
			targetSecretName1 = fw.AsKubeDeveloper.SPIController.GetTargetSecretName(targets, targetNamespace1)
			Expect(targetSecretName1).ToNot(BeEmpty())

			// get targetSecretName12
			targetSecretName2 = fw.AsKubeDeveloper.SPIController.GetTargetSecretName(targets, targetNamespace2)
			Expect(targetSecretName2).ToNot(BeEmpty())
		})

		It("checks if secret was created in target namespaces", func() {
			_, err = fw.AsKubeAdmin.CommonController.GetSecret(targetNamespace1, targetSecretName1)
			Expect(err).NotTo(HaveOccurred())

			_, err = fw.AsKubeAdmin.CommonController.GetSecret(targetNamespace2, targetSecretName2)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("SVPI-558 - Target to the same namespace where the remote secret lives is always deployed", Ordered, func() {
		var fw *framework.Framework
		var err error
		var namespace string
		var remoteSecret *v1beta1.RemoteSecret
		remoteSecretName := fmt.Sprintf("test-remote-secret-%s", util.GenerateRandomString(4))
		targetSecretName := ""

		BeforeAll(func() {
			// Initialize the tests controllers
			fw, err = framework.NewFramework(utils.GetGeneratedNamespace("spi-demos"))
			Expect(err).NotTo(HaveOccurred())
			namespace = fw.UserNamespace
			Expect(namespace).NotTo(BeEmpty())
		})

		AfterAll(func() {
			if !CurrentSpecReport().Failed() {
				Expect(fw.SandboxController.DeleteUserSignup(fw.UserName)).To(BeTrue())
			}
		})

		It("creates RemoteSecret with a target that shares the same namespace", func() {
			remoteSecret, err = fw.AsKubeDeveloper.SPIController.CreateRemoteSecret(remoteSecretName, namespace, []string{namespace})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				remoteSecret, err = fw.AsKubeDeveloper.SPIController.GetRemoteSecret(remoteSecretName, namespace)
				Expect(err).NotTo(HaveOccurred())

				return meta.IsStatusConditionFalse(remoteSecret.Status.Conditions, "DataObtained")
			}, 5*time.Minute, 5*time.Second).Should(BeTrue(), fmt.Sprintf("RemoteSecret %s/%s is not waiting for data", namespace, remoteSecretName))
		})

		It("creates upload secret", func() {
			s := &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-secret",
					Labels: map[string]string{
						"appstudio.redhat.com/upload-secret": "remotesecret",
					},
					Annotations: map[string]string{
						"appstudio.redhat.com/remotesecret-name": remoteSecret.Name,
					},
				},
				Type: v1.SecretTypeOpaque,
				StringData: map[string]string{
					"a": "b",
					"c": "d",
				},
			}

			_, err = fw.AsKubeAdmin.CommonController.CreateSecret(namespace, s)
			Expect(err).NotTo(HaveOccurred())
		})

		It("checks if remote secret was deployed", func() {
			Eventually(func() bool {
				remoteSecret, err = fw.AsKubeDeveloper.SPIController.GetRemoteSecret(remoteSecretName, namespace)
				Expect(err).NotTo(HaveOccurred())

				return meta.IsStatusConditionTrue(remoteSecret.Status.Conditions, "Deployed")
			}, 5*time.Minute, 5*time.Second).Should(BeTrue(), fmt.Sprintf("RemoteSecret %s/%s is not in deployed phase", namespace, remoteSecretName))
		})

		It("checks targets in RemoteSecret status", func() {
			remoteSecret, err = fw.AsKubeDeveloper.SPIController.GetRemoteSecret(remoteSecret.Name, namespace)
			Expect(err).NotTo(HaveOccurred())

			targets := remoteSecret.Status.Targets
			Expect(targets).To(HaveLen(1))

			// get targetSecretName
			targetSecretName = fw.AsKubeDeveloper.SPIController.GetTargetSecretName(targets, namespace)
			Expect(targetSecretName).ToNot(BeEmpty())
		})

		It("checks if secret was created in target namespace", func() {
			_, err = fw.AsKubeAdmin.CommonController.GetSecret(namespace, targetSecretName)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
