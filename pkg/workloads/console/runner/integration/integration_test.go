package integration

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha1 "github.com/gocardless/theatre/apis/workloads/v1alpha1"
	"github.com/gocardless/theatre/pkg/workloads/console/runner"
)

func newClientset() *kubernetes.Clientset {
	clientset, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred(), "could not connect to kubernetes cluster")

	return clientset
}

func newNamespace(name string) *corev1.Namespace {
	if name == "" {
		name = uuid.New().String()
	}
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func newConsoleTemplate(namespace, name string, labels map[string]string) *workloadsv1alpha1.ConsoleTemplate {
	return &workloadsv1alpha1.ConsoleTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: workloadsv1alpha1.ConsoleTemplateSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						corev1.Container{
							Image: "alpine:latest",
							Name:  "console-container-0",
						},
					},
				},
			},
		},
	}
}

func newConsole(namespace, name, consoleTemplateName, username string, labels map[string]string) *workloadsv1alpha1.Console {
	return &workloadsv1alpha1.Console{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: workloadsv1alpha1.ConsoleSpec{
			User: username,
			ConsoleTemplateRef: corev1.LocalObjectReference{
				Name: consoleTemplateName,
			},
		},
	}
}

func newRoleBinding(namespace, name, username string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "User",
				Name: username,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "Role",
			Name: "console-test",
		},
	}

}

var _ = Describe("Runner", func() {
	var (
		consoleRunner             *runner.Runner
		mustCreateConsole         func(*workloadsv1alpha1.Console)
		mustCreateConsoleTemplate func(*workloadsv1alpha1.ConsoleTemplate)
		mustCreateNamespace       func(*corev1.Namespace)
		mustCreateRoleBinding     func(*rbacv1.RoleBinding)
	)

	JustBeforeEach(func() {
		consoleRunner = runner.New(newClientset(), kubeClient)

		mustCreateNamespace = func(namespace *corev1.Namespace) {
			By("Creating test namespace: " + namespace.Name)
			Expect(kubeClient.Create(context.TODO(), namespace)).NotTo(
				HaveOccurred(), "failed to create test namespace",
			)
		}

		mustCreateConsoleTemplate = func(consoleTemplate *workloadsv1alpha1.ConsoleTemplate) {
			By("Creating console template: " + consoleTemplate.Name)
			Expect(kubeClient.Create(context.TODO(), consoleTemplate)).NotTo(
				HaveOccurred(), "failed to create console template",
			)
		}

		mustCreateConsole = func(console *workloadsv1alpha1.Console) {
			By("Creating console: " + console.Name)
			Expect(kubeClient.Create(context.TODO(), console)).NotTo(
				HaveOccurred(), "failed to create console ",
			)
		}

		mustCreateRoleBinding = func(roleBinding *rbacv1.RoleBinding) {
			By("Creating role binding: " + roleBinding.Name)
			Expect(kubeClient.Create(context.TODO(), roleBinding)).NotTo(
				HaveOccurred(), "failed to create role binding",
			)
		}
	})

	Describe("CreateResource", func() {
		var (
			console         *workloadsv1alpha1.Console
			consoleTemplate *workloadsv1alpha1.ConsoleTemplate
			err             error
			namespace       *corev1.Namespace
		)

		cmd := []string{"/bin/rails", "console"}
		reason := "reason for console"
		createOptions := runner.Options{Cmd: cmd, Reason: reason}

		JustBeforeEach(func() {
			namespace = newNamespace("")
			mustCreateNamespace(namespace)

			consoleTemplate = newConsoleTemplate(namespace.Name, "test", map[string]string{"release": "test"})
			console, err = consoleRunner.CreateResource(namespace.Name, *consoleTemplate, createOptions)
		})

		Context("When creating a new console", func() {
			It("Creates a console", func() {
				Expect(err).NotTo(HaveOccurred())
				Expect(console).NotTo(BeNil(), "a console was not returned")
			})

			It("References the template in the returned console spec", func() {
				Expect(console.Spec.ConsoleTemplateRef.Name).To(Equal(consoleTemplate.Name))
			})

			It("Sets the specified command in the spec", func() {
				Expect(console.Spec.Command).To(Equal(cmd))
			})

			It("Sets the specified reason in the spec", func() {
				Expect(console.Spec.Reason).To(Equal(reason))
			})

			It("Creates the console via the clientset", func() {
				Eventually(func() []workloadsv1alpha1.Console {
					opts := &client.ListOptions{Namespace: namespace.Name}
					consoleList := &workloadsv1alpha1.ConsoleList{}
					kubeClient.List(context.TODO(), consoleList, opts)
					return consoleList.Items
				}).Should(HaveLen(1), "only one console should be present")
			})

			It("Inherits labels from console template", func() {
				Expect(console.Labels).To(HaveKeyWithValue("release", "test"))
			})

			It("Creates the console in the namespace specified", func() {
				Eventually(func() error {
					err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: namespace.Name, Name: console.Name}, console)
					return err
				}).ShouldNot(HaveOccurred(), "failed to get console")
			})
		})
	})

	Describe("FindTemplateBySelector", func() {
		var (
			consoleTemplate *workloadsv1alpha1.ConsoleTemplate
			namespace       *corev1.Namespace
		)

		JustBeforeEach(func() {
			namespace = newNamespace("")
			consoleTemplate = newConsoleTemplate(namespace.Name, "test", map[string]string{"release": "test"})

			mustCreateNamespace(namespace)
			mustCreateConsoleTemplate(consoleTemplate)
		})

		It("Finds a template across all namespaces", func() {
			foundTmpl, err := consoleRunner.FindTemplateBySelector(metav1.NamespaceAll, "release=test")
			Expect(err).NotTo(HaveOccurred(), "unable to find template")
			Expect(foundTmpl.Name).To(Equal(consoleTemplate.Name))
		})

		It("Finds a template in a single namespace", func() {
			foundTmpl, err := consoleRunner.FindTemplateBySelector(namespace.Name, "release=test")
			Expect(err).NotTo(HaveOccurred(), "unable to find template")
			Expect(foundTmpl.Name).To(Equal(consoleTemplate.Name))
		})

		Context("When searching for a non-existent template", func() {
			It("Returns an error", func() {
				foundTmpl, err := consoleRunner.FindTemplateBySelector(namespace.Name, "release=not-here")
				Expect(err).To(HaveOccurred(), "should be unable to find template")
				Expect(foundTmpl).To(BeNil(), "result template should be nil")
			})
		})

		Context("With multiple colliding templates", func() {
			It("Fails when targeting all namespaces", func() {
				_, err := consoleRunner.FindTemplateBySelector(metav1.NamespaceAll, "release=test")
				Expect(err).To(HaveOccurred(), "expected template collision error")
				Expect(err.Error()).To(
					ContainSubstring("expected to discover 1 console template"),
					"error should list conflicting templates",
				)
			})
		})
	})

	Describe("WaitUntilReady", func() {
		var (
			console         *workloadsv1alpha1.Console
			consoleTemplate *workloadsv1alpha1.ConsoleTemplate
			namespace       *corev1.Namespace
			phase           workloadsv1alpha1.ConsolePhase
			roleBinding     *rbacv1.RoleBinding
		)

		timeout := 3 * time.Second

		updateConsolePhase := func(in *workloadsv1alpha1.Console, phase workloadsv1alpha1.ConsolePhase) {
			csl := &workloadsv1alpha1.Console{}
			err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: in.Namespace, Name: in.Name}, csl)
			Expect(err).ToNot(HaveOccurred(), "error while retrieving console")

			csl.Status.Phase = phase
			err = kubeClient.Update(context.TODO(), csl)
			Expect(err).ToNot(HaveOccurred(), "error while updating console status")
		}

		addSubjectsToRoleBinding := func(rb *rbacv1.RoleBinding, subjects []rbacv1.Subject) {
			rb.Subjects = subjects
			err := kubeClient.Update(context.TODO(), rb)
			Expect(err).NotTo(HaveOccurred())
		}

		JustBeforeEach(func() {
			namespace = newNamespace("")
			mustCreateNamespace(namespace)

			consoleTemplate = newConsoleTemplate(namespace.Name, "test", map[string]string{})
			mustCreateConsoleTemplate(consoleTemplate)

			console = newConsole(namespace.Name, "test", consoleTemplate.Name, "test-user", map[string]string{})
			console.Status.Phase = phase
			mustCreateConsole(console)

			roleBinding = newRoleBinding(namespace.Name, console.Name, console.Spec.User)
			mustCreateRoleBinding(roleBinding)
		})

		Context("When phase is pending", func() {
			BeforeEach(func() {
				phase = workloadsv1alpha1.ConsolePending
			})

			It("Fails with a timeout waiting", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)
				Expect(err.Error()).To(ContainSubstring("last phase was: Pending"))
				Expect(ctx.Err()).To(MatchError(context.DeadlineExceeded), "context should have timed out")
			})
		})

		Context("When phase is updated to Running", func() {
			BeforeEach(func() {
				phase = workloadsv1alpha1.ConsolePending
			})

			It("Returns successfully", func() {
				// Give some time for the watch to be set up, by waiting until
				// half-way through the timeout period before updating the object.
				time.AfterFunc(timeout/2,
					func() { updateConsolePhase(console, workloadsv1alpha1.ConsoleRunning) },
				)

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				upToDateCsl, err := consoleRunner.WaitUntilReady(ctx, *console, true)
				Expect(err).ToNot(HaveOccurred())
				Expect(upToDateCsl.Status.Phase).To(Equal(workloadsv1alpha1.ConsoleRunning))
			})
		})

		Context("When phase is updated to non-Running", func() {
			BeforeEach(func() {
				phase = workloadsv1alpha1.ConsolePending
			})

			It("Returns with a failure before the timeout", func() {
				time.AfterFunc(timeout/2,
					func() { updateConsolePhase(console, workloadsv1alpha1.ConsoleStopped) },
				)

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err.Error()).To(ContainSubstring("console is stopped"))
				Expect(ctx.Err()).To(BeNil(), "context should not have timed out")
			})
		})

		Context("When console is already running", func() {
			BeforeEach(func() {
				phase = workloadsv1alpha1.ConsoleRunning
			})

			It("Returns successfully", func() {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				upToDateCsl, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err).ToNot(HaveOccurred())
				Expect(upToDateCsl.Status.Phase).To(Equal(workloadsv1alpha1.ConsoleRunning))
			})
		})

		Context("When console is already stopped", func() {
			BeforeEach(func() {
				phase = workloadsv1alpha1.ConsoleStopped
			})

			// TODO - return a proper error
			It("Returns an error immediately", func() {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(ctx.Err()).To(BeNil(), "context should not have timed out")
				Expect(err.Error()).To(ContainSubstring("console is stopped"))
			})
		})

		Context("When console does not exist", func() {
			It("Fails with a timeout", func() {
				console.Name = "idontexist"
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err.Error()).To(ContainSubstring("context deadline exceeded"))
				Expect(ctx.Err()).To(MatchError(context.DeadlineExceeded), "context should have timed out")
			})
		})

		Context("But it is later created", func() {
			createCsl := func() {
				defer GinkgoRecover()
				mustCreateConsole(console)
			}

			It("Returns successfully", func() {
				console = newConsole(namespace.Name, "idontexistyet", consoleTemplate.Name, "test-user", map[string]string{})
				console.Status.Phase = workloadsv1alpha1.ConsoleRunning
				time.AfterFunc(timeout/2, createCsl)

				roleBinding := newRoleBinding(namespace.Name, console.Name, console.Spec.User)
				mustCreateRoleBinding(roleBinding)

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				upToDateCsl, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err).ToNot(HaveOccurred())
				Expect(upToDateCsl.Status.Phase).To(Equal(workloadsv1alpha1.ConsoleRunning))
			})
		})

		Describe("Waiting for the rolebinding to be ready", func() {
			Context("When the rolebinding does not exist yet", func() {
				It("Fails with a timeout", func() {
					console = newConsole(namespace.Name, "consolewithoutrolebinding", consoleTemplate.Name, "test-user", map[string]string{})
					console.Status.Phase = workloadsv1alpha1.ConsoleRunning
					mustCreateConsole(console)

					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					defer cancel()
					_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

					Expect(err).To(MatchError(ContainSubstring("waiting for rolebinding interrupted")))
					Expect(ctx.Err()).To(MatchError(context.DeadlineExceeded), "context should have timed out")
				})
			})
		})

		Context("When it is subsequently created then updated", func() {
			createRoleBinding := func() {
				defer GinkgoRecover()
				mustCreateRoleBinding(roleBinding)
			}

			It("Returns success", func() {
				console = newConsole(namespace.Name, "consolewithoutrolebinding", consoleTemplate.Name, "test-user", map[string]string{})
				console.Status.Phase = workloadsv1alpha1.ConsoleRunning
				mustCreateConsole(console)

				roleBinding = newRoleBinding(namespace.Name, console.Name, console.Spec.User)

				time.AfterFunc(timeout/2, func() {
					defer GinkgoRecover()
					createRoleBinding()
				})

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("When the rolebinding exists but has no subjects", func() {
			It("Fails with a timeout", func() {
				console = newConsole(namespace.Name, "norolebindingsubjects", consoleTemplate.Name, "test-user", map[string]string{})
				console.Status.Phase = workloadsv1alpha1.ConsoleRunning
				mustCreateConsole(console)

				roleBinding = newRoleBinding(namespace.Name, console.Name, console.Spec.User)
				roleBinding.Subjects = nil
				mustCreateRoleBinding(roleBinding)

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err).To(MatchError(ContainSubstring("waiting for rolebinding interrupted")))
				Expect(ctx.Err()).To(MatchError(context.DeadlineExceeded), "context should have timed out")
			})
		})

		Context("When it is subsequently updated with the desired subjects", func() {
			It("Returns success", func() {
				console = newConsole(namespace.Name, "norolebindingsubjects", consoleTemplate.Name, "test-user", map[string]string{})
				console.Status.Phase = workloadsv1alpha1.ConsoleRunning
				mustCreateConsole(console)

				roleBinding = newRoleBinding(namespace.Name, console.Name, console.Spec.User)
				roleBinding.Subjects = nil
				mustCreateRoleBinding(roleBinding)

				time.AfterFunc(timeout/2,
					func() {
						defer GinkgoRecover()
						addSubjectsToRoleBinding(roleBinding, []rbacv1.Subject{{Kind: "User", Name: console.Spec.User}})
					},
				)

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("When it is subsequently updated with undesired subjects", func() {
			It("Fails with a timeout", func() {
				console = newConsole(namespace.Name, "norolebindingsubjects", consoleTemplate.Name, "test-user", map[string]string{})
				console.Status.Phase = workloadsv1alpha1.ConsoleRunning
				mustCreateConsole(console)

				roleBinding = newRoleBinding(namespace.Name, console.Name, console.Spec.User)
				roleBinding.Subjects = nil
				mustCreateRoleBinding(roleBinding)

				time.AfterFunc(timeout/2,
					func() {
						defer GinkgoRecover()
						addSubjectsToRoleBinding(roleBinding, []rbacv1.Subject{{Kind: "User", Name: "rando"}})
					},
				)

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				_, err := consoleRunner.WaitUntilReady(ctx, *console, true)

				Expect(err).To(MatchError(ContainSubstring("waiting for rolebinding interrupted")))
				Expect(ctx.Err()).To(MatchError(context.DeadlineExceeded), "context should have timed out")
			})
		})
	})
})