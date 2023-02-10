package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	resourcesclient "github.com/bentoml/yatai-image-builder/generated/resources/clientset/versioned/typed/resources/v1alpha1"

	//nolint:golint
	//nolint:revive
	. "github.com/onsi/ginkgo/v2"

	//nolint:golint
	//nolint:revive
	. "github.com/onsi/gomega"

	"github.com/bentoml/yatai-deployment/tests/utils"
)

var _ = Describe("yatai-deployment", Ordered, func() {
	var daemonProcess *os.Process

	AfterAll(func() {
		By("Showing yatai-deployment logs")
		cmd := exec.Command("kubectl", "-n", "yatai-deployment", "logs", "--tail", "200", "-l", "app.kubernetes.io/name=yatai-deployment")
		logs, _ := utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing yatai-deployment events")
		cmd = exec.Command("kubectl", "-n", "yatai-deployment", "describe", "pod", "-l", "app.kubernetes.io/name=yatai-deployment")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment pods events")
		cmd = exec.Command("kubectl", "-n", "yatai", "describe", "pod", "-l", "yatai.ai/bento-deployment=test")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment api-server pods main container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test", "-c", "main")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment api-server pods proxy container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test", "-c", "proxy")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment api-server pods metrics-transformer container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test", "-c", "metrics-transformer")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment api-server pods monitor-exporter container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test", "-c", "monitor-exporter")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment runner pods main container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test-runner-0", "-c", "main")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment runner pods metrics-transformer container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test-runner-0", "-c", "metrics-transformer")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Showing BentoDeployment runner pods monitor-exporter container logs")
		cmd = exec.Command("kubectl", "-n", "yatai", "logs", "deploy/test-runner-0", "-c", "monitor-exporter")
		logs, _ = utils.Run(cmd)
		fmt.Println(string(logs))
		By("Stopping the port-forward daemon process")
		if daemonProcess != nil {
			err := daemonProcess.Kill()
			Expect(err).To(BeNil())
		}
		if os.Getenv("E2E_CHECK_NAME") != "" {
			By("Cleaning up BentoDeployment resources")
			cmd = exec.Command("kubectl", "delete", "-f", "tests/e2e/example.yaml")
			_, _ = utils.Run(cmd)
		}
	})

	Context("BentoDeployment Operator", func() {
		It("Should run successfully", func() {
			By("Creating a BentoDeployment CR")
			cmd := exec.Command("kubectl", "apply", "-f", "tests/e2e/example.yaml")
			out, err := utils.Run(cmd)
			Expect(err).To(BeNil(), "Failed to create BentoDeployment CR: %s", string(out))

			By("Sleeping for 5 seconds")
			time.Sleep(5 * time.Second)

			By("Waiting for the bento api-server deployment to be available")
			cmd = exec.Command("kubectl", "-n", "yatai", "wait", "--for", "condition=available", "--timeout", "5m", "deployment/test")
			out, err = utils.Run(cmd)
			Expect(err).To(BeNil(), "Failed to wait for the bento api-server deployment to be available: %s", string(out))

			By("Waiting for the bento runner deployment to be available")
			cmd = exec.Command("kubectl", "-n", "yatai", "wait", "--for", "condition=available", "--timeout", "5m", "deployment/test-runner-0")
			out, err = utils.Run(cmd)
			Expect(err).To(BeNil(), "Failed to wait for the bento runner deployment to be available: %s", string(out))

			restConf := config.GetConfigOrDie()
			cliset, err := kubernetes.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create kubernetes clientset")

			bentorequestcli, err := resourcesclient.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create bentorequest clientset")

			By("Checking the bento api-server deployment image name")
			ctx := context.Background()

			logrus.Infof("Getting Bento CR %s", "test-bento")
			bento, err := bentorequestcli.Bentoes("yatai").Get(ctx, "test-bento", metav1.GetOptions{})
			Expect(err).To(BeNil(), "failed to get Bento CR %s", "test-bento")

			logrus.Infof("Getting deployment %s", "test")
			deployment, err := cliset.AppsV1().Deployments("yatai").Get(ctx, "test", metav1.GetOptions{})
			Expect(err).To(BeNil(), "Failed to get deployment %s", "test")

			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(bento.Spec.Image), "bento api-server deployment image name is not correct")

			By("Port-forwarding the bento api-server service")
			cmd = exec.Command("kubectl", "-n", "yatai", "port-forward", "svc/test", "3000:3000")
			daemonProcess, err = utils.RunAsDaemon(cmd)
			Expect(err).To(BeNil(), "Failed to port-forward the bento api-server service")

			By("Sleeping for 5 seconds")
			time.Sleep(5 * time.Second)

			By("Sending a request to the bento api-server")
			EventuallyWithOffset(1, func() error {
				req, err := http.NewRequest("POST", "http://localhost:3000/classify", bytes.NewBuffer([]byte(`[[0,1,2,3]]`)))
				if err != nil {
					return err
				}
				req.Header.Set("Content-Type", "application/json")
				client := &http.Client{}
				resp, err := client.Do(req)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = errors.Errorf("unexpected status code: %d", resp.StatusCode)
					return err
				}
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				Expect(string(body)).To(Equal(`[2]`))
				return nil
			}).Should(Succeed())
		})
	})
})
