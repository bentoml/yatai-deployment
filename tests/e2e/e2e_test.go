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
		By("Stopping the daemon process")
		if daemonProcess != nil {
			err := daemonProcess.Kill()
			Expect(err).To(BeNil())
		}
		By("Cleaning up BentoDeployment resources")
		cmd := exec.Command("kubectl", "delete", "-f", "tests/e2e/example.yaml")
		_, _ = utils.Run(cmd)
		By("Showing yatai-deployment logs")
		cmd = exec.Command("kubectl", "logs", "-n", "yatai-deployment", "-l", "app.kubernetes.io/name=yatai-deployment")
		logs, _ := utils.Run(cmd)
		fmt.Println(string(logs))
	})

	Context("BentoDeployment Operator", func() {
		It("should run successfully", func() {
			By("creating a BentoDeployment CR")
			EventuallyWithOffset(1, func() error {
				cmd := exec.Command("kubectl", "apply", "-f", "tests/e2e/example.yaml")
				_, err := utils.Run(cmd)
				return err
			}, time.Minute, time.Second).Should(Succeed())

			By("Sleeping for 5 seconds")
			time.Sleep(5 * time.Second)

			By("Waiting for the bento api-server deployment to be available")
			EventuallyWithOffset(1, func() error {
				cmd := exec.Command("kubectl", "-n", "yatai", "wait", "--for", "condition=available", "--timeout", "600s", "deployment/test")
				_, err := utils.Run(cmd)
				return err
			}).Should(Succeed())

			By("Waiting for the bento runner deployment to be available")
			EventuallyWithOffset(1, func() error {
				cmd := exec.Command("kubectl", "-n", "yatai", "wait", "--for", "condition=available", "--timeout", "600s", "deployment/test-runner-0")
				_, err := utils.Run(cmd)
				return err
			}).Should(Succeed())

			restConf := config.GetConfigOrDie()
			cliset, err := kubernetes.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create kubernetes clientset")

			bentorequestcli, err := resourcesclient.NewForConfig(restConf)
			Expect(err).To(BeNil(), "failed to create bentorequest clientset")

			By("Checking the bento api-server deployment image name")
			EventuallyWithOffset(1, func() error {
				ctx := context.Background()

				logrus.Infof("Getting Bento CR %s", "test-bento")
				bento, err := bentorequestcli.Bentoes("yatai").Get(ctx, "test-bento", metav1.GetOptions{})
				if err != nil {
					return err
				}

				logrus.Infof("Getting deployment %s", "test")
				deployment, err := cliset.AppsV1().Deployments("yatai").Get(ctx, "test", metav1.GetOptions{})
				if err != nil {
					return err
				}

				Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(bento.Spec.Image), "bento api-server deployment image name is not correct")
				return nil
			}).Should(Succeed())

			By("Port-forwarding the bento api-server service")
			EventuallyWithOffset(1, func() error {
				cmd := exec.Command("kubectl", "-n", "yatai", "port-forward", "svc/test", "3000:3000")
				var err error
				daemonProcess, err = utils.RunAsDaemon(cmd)
				return err
			}).Should(Succeed())

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
