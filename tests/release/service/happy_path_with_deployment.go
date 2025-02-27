package service

import (
	"fmt"
	"os"
	"time"

	ecp "github.com/enterprise-contract/enterprise-contract-controller/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appservice "github.com/redhat-appstudio/application-api/api/v1alpha1"
	"github.com/redhat-appstudio/e2e-tests/pkg/clients/has"
	"github.com/redhat-appstudio/e2e-tests/pkg/constants"
	"github.com/redhat-appstudio/e2e-tests/pkg/framework"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils/contract"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils/tekton"
	releaseConst "github.com/redhat-appstudio/e2e-tests/tests/release"
	releaseApi "github.com/redhat-appstudio/release-service/api/v1alpha1"
	tektonutils "github.com/redhat-appstudio/release-service/tekton/utils"
	corev1 "k8s.io/api/core/v1"
)

var _ = framework.ReleaseServiceSuiteDescribe("happy_path_with_deployment", Label("release-service", "withDeployment"), func() {
	defer GinkgoRecover()
	// Initialize the tests controllers
	var fw *framework.Framework
	AfterEach(framework.ReportFailure(&fw))
	var err error
	var compName string
	var devNamespace string

	var component *appservice.Component
	var releaseCR *releaseApi.Release
	var managedNamespace string
	var releaseEnvironment = "production"
	var releaseDeploymentTimeout = 10 * time.Minute
	var releaseFinishedTimeout = 5 * time.Minute

	BeforeAll(func() {
		fw, err = framework.NewFramework(utils.GetGeneratedNamespace("happy-depl"))
		Expect(err).NotTo(HaveOccurred())
		devNamespace = fw.UserNamespace
		managedNamespace = utils.GetGeneratedNamespace("happy-depl-managed")
		_, err = fw.AsKubeAdmin.CommonController.CreateTestNamespace(managedNamespace)
		Expect(err).NotTo(HaveOccurred(), "Error when creating managedNamespace: %v", err)

		sourceAuthJson := utils.GetEnv("QUAY_TOKEN", "")
		Expect(sourceAuthJson).ToNot(BeEmpty())

		_, err = fw.AsKubeAdmin.CommonController.CreateRegistryAuthSecret(releaseConst.RedhatAppstudioUserSecret, managedNamespace, sourceAuthJson)
		Expect(err).ToNot(HaveOccurred())

		err = fw.AsKubeAdmin.CommonController.LinkSecretToServiceAccount(devNamespace, releaseConst.HacbsReleaseTestsTokenSecret, constants.DefaultPipelineServiceAccount, true)
		Expect(err).ToNot(HaveOccurred())

		publicKey, err := fw.AsKubeAdmin.TektonController.GetTektonChainsPublicKey()
		Expect(err).ToNot(HaveOccurred())

		Expect(fw.AsKubeAdmin.TektonController.CreateOrUpdateSigningSecret(
			publicKey, releaseConst.PublicSecretNameAuth, managedNamespace)).To(Succeed())

		defaultEcPolicy, err := fw.AsKubeAdmin.TektonController.GetEnterpriseContractPolicy("default", "enterprise-contract-service")
		Expect(err).NotTo(HaveOccurred())
		policy := contract.PolicySpecWithSourceConfig(defaultEcPolicy.Spec, ecp.SourceConfig{Include: []string{"@minimal"}, Exclude: []string{"cve"}})

		// using cdq since git ref is not known
		compName = releaseConst.ComponentName
		var componentDetected appservice.ComponentDetectionDescription
		cdq, err := fw.AsKubeAdmin.HasController.CreateComponentDetectionQuery(compName, devNamespace, releaseConst.GitSourceComponentUrl, "", "", "", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(cdq.Status.ComponentDetected).To(HaveLen(1), "Expected length of the detected Components was not 1")

		for _, compDetected := range cdq.Status.ComponentDetected {
			compName = compDetected.ComponentStub.ComponentName
			componentDetected = compDetected
		}

		_, err = fw.AsKubeAdmin.ReleaseController.CreateReleasePlan(releaseConst.SourceReleasePlanName, devNamespace, releaseConst.ApplicationNameDefault, managedNamespace, "")
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.GitOpsController.CreatePocEnvironment(releaseEnvironment, managedNamespace)
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.ReleaseController.CreateReleasePlanAdmission(releaseConst.TargetReleasePlanAdmissionName, managedNamespace, releaseEnvironment, devNamespace, releaseConst.ReleaseStrategyPolicyDefault, releaseConst.ReleasePipelineServiceAccountDefault, []string{releaseConst.ApplicationNameDefault}, true, &tektonutils.PipelineRef{
			Resolver: "git",
			Params: []tektonutils.Param{
				{Name: "url", Value: "https://github.com/redhat-appstudio/release-service-catalog"},
				{Name: "revision", Value: "main"},
				{Name: "pathInRepo", Value: "pipelines/e2e/e2e.yaml"},
			},
		}, nil)
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.TektonController.CreateEnterpriseContractPolicy(releaseConst.ReleaseStrategyPolicyDefault, managedNamespace, policy)
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.TektonController.CreatePVCInAccessMode(releaseConst.ReleasePvcName, managedNamespace, corev1.ReadWriteOnce)
		Expect(err).NotTo(HaveOccurred())

		managedServiceAccount, err := fw.AsKubeAdmin.CommonController.CreateServiceAccount(releaseConst.ReleasePipelineServiceAccountDefault, managedNamespace, releaseConst.ManagednamespaceSecret, nil)
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.ReleaseController.CreateReleasePipelineRoleBindingForServiceAccount(devNamespace, managedServiceAccount)
		Expect(err).NotTo(HaveOccurred())
		_, err = fw.AsKubeAdmin.ReleaseController.CreateReleasePipelineRoleBindingForServiceAccount(managedNamespace, managedServiceAccount)
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.CommonController.CreateRole("role-release-service-account", managedNamespace, map[string][]string{
			"apiGroupsList": {""},
			"roleResources": {"secrets"},
			"roleVerbs":     {"get", "list", "watch"},
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.CommonController.CreateRoleBinding("role-release-service-account-binding", managedNamespace, "ServiceAccount", releaseConst.ReleasePipelineServiceAccountDefault, managedNamespace, "Role", "role-release-service-account", "rbac.authorization.k8s.io")
		Expect(err).NotTo(HaveOccurred())

		_, err = fw.AsKubeAdmin.HasController.CreateApplication(releaseConst.ApplicationNameDefault, devNamespace)
		Expect(err).NotTo(HaveOccurred())

		component, err = fw.AsKubeAdmin.HasController.CreateComponent(componentDetected.ComponentStub, devNamespace, "", "", releaseConst.ApplicationNameDefault, true, map[string]string{})
		Expect(err).NotTo(HaveOccurred())

		workingDir, err := os.Getwd()
		if err != nil {
			GinkgoWriter.Printf(err.Error())
		}

		// Download copy-applications.sh script from release-utils repo
		scriptFileName := "copy-application.sh"
		args := []string{"https://raw.githubusercontent.com/hacbs-release/release-utils/main/copy-application.sh", "-o", scriptFileName}
		Expect(utils.ExecuteCommandInASpecificDirectory("curl", args, workingDir)).To(Succeed())
		defer os.Remove(fmt.Sprintf("%s/%s", workingDir, scriptFileName))

		args = []string{"775", "copy-application.sh"}
		Expect(utils.ExecuteCommandInASpecificDirectory("chmod", args, workingDir)).To(Succeed())
		// Copying application in dev namespace to managed namespace
		args = []string{managedNamespace, "-a", devNamespace + "/" + releaseConst.ApplicationNameDefault}
		Expect(utils.ExecuteCommandInASpecificDirectory("./copy-application.sh", args, workingDir)).To(Succeed())

	})

	AfterAll(func() {
		if !CurrentSpecReport().Failed() {
			Expect(fw.AsKubeAdmin.CommonController.DeleteNamespace(managedNamespace)).NotTo(HaveOccurred())
			Expect(fw.SandboxController.DeleteUserSignup(fw.UserName)).To(BeTrue())
		}
	})

	var _ = Describe("Post-release verification", func() {

		It("verifies that a build PipelineRun is created in dev namespace and succeeds", func() {
			Expect(fw.AsKubeAdmin.HasController.WaitForComponentPipelineToBeFinished(component, "",
				fw.AsKubeAdmin.TektonController, &has.RetryOptions{Retries: 2, Always: true})).To(Succeed())
		})

		It("tests an associated Release CR is created", func() {
			Eventually(func() error {
				releaseCR, err = fw.AsKubeAdmin.ReleaseController.GetFirstReleaseInNamespace(devNamespace)
				return err
			}, releaseConst.ReleaseCreationTimeout, releaseConst.DefaultInterval).Should(Succeed())
		})

		It("verifies that Release PipelineRun is triggered", func() {
			Eventually(func() error {
				pr, err := fw.AsKubeAdmin.ReleaseController.GetPipelineRunInNamespace(managedNamespace, releaseCR.GetName(), releaseCR.GetNamespace())
				if err != nil {
					GinkgoWriter.Printf("release pipelineRun for release '%s' in namespace '%s' not created yet: %+v\n", releaseCR.GetName(), releaseCR.GetNamespace(), err)
					return err
				}
				if !pr.HasStarted() {
					return fmt.Errorf("pipelinerun %s/%s hasn't started yet", pr.GetNamespace(), pr.GetName())
				}
				return nil
			}, releaseConst.ReleasePipelineRunCreationTimeout, releaseConst.DefaultInterval).Should(Succeed(), fmt.Sprintf("timed out waiting for a pipelinerun to start for a release %s/%s", releaseCR.GetName(), releaseCR.GetNamespace()))
		})

		It("verifies that Release PipelineRun should eventually succeed", func() {
			Eventually(func() error {
				pr, err := fw.AsKubeAdmin.ReleaseController.GetPipelineRunInNamespace(managedNamespace, releaseCR.GetName(), releaseCR.GetNamespace())
				Expect(err).ShouldNot(HaveOccurred())
				if !pr.IsDone() {
					return fmt.Errorf("release pipelinerun %s/%s did not finish yet", pr.GetNamespace(), pr.GetName())
				}
				Expect(tekton.HasPipelineRunSucceeded(pr)).To(BeTrue(), fmt.Sprintf("release pipelinerun %s/%s did not succeed", pr.GetNamespace(), pr.GetName()))
				return nil
			}, releaseConst.ReleasePipelineRunCompletionTimeout, releaseConst.DefaultInterval).Should(Succeed())
		})
		// phase Deployed happens before phase Released
		It("tests a Release CR reports that the deployment was successful", func() {
			Eventually(func() error {
				releaseCR, err := fw.AsKubeAdmin.ReleaseController.GetFirstReleaseInNamespace(devNamespace)
				if err != nil {
					return err
				}
				if !releaseCR.IsDeployed() {
					return fmt.Errorf("release %s/%s is not marked as deployed yet", releaseCR.GetNamespace(), releaseCR.GetName())
				}
				return nil
			}, releaseDeploymentTimeout, releaseConst.DefaultInterval).Should(Succeed())
		})
		It("tests a Release CR is marked as successfully deployed", func() {
			Eventually(func() error {
				releaseCR, err := fw.AsKubeAdmin.ReleaseController.GetFirstReleaseInNamespace(devNamespace)
				if err != nil {
					return err
				}
				if !releaseCR.IsReleased() {
					return fmt.Errorf("release %s/%s is not marked as finished yet", releaseCR.GetNamespace(), releaseCR.GetName())
				}
				return nil
			}, releaseFinishedTimeout, releaseConst.DefaultInterval).Should(Succeed())
		})
	})
})
