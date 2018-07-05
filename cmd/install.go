package cmd

import (
	"fmt"
	"path"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/chanzuckerberg/czecs/tasks"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

var (
	quiet    bool
	rollback bool
	service  string
)

// installCmd represents the install command
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install a service into an ECS cluster",
	Long: `This command installs a service into an ECS cluster.

Limitations: No support for setting up load balancers through this command;
if you need load balancers; manually create an ECS service outside this tool
(e.g. using Terraform or aws command line tool), then use czecs upgrade.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cluster := args[0]
		czecsPath := args[1]
		var balances map[string]interface{}
		balances, err := mergeValues(balanceFiles, values, stringValues)
		if err != nil {
			return err
		}
		values := map[string]interface{}{
			"Values": balances,
		}
		registerTaskDefinitionInput, err := tasks.ParseTaskDefinition(path.Join(czecsPath, "czecs.json"), values, strict)
		if err != nil {
			return errors.Wrap(err, "cannot parse task definition")
		}

		if debug {
			fmt.Printf("%+v\n", registerTaskDefinitionInput)
		}
		sess := session.Must(session.NewSessionWithOptions(session.Options{
			SharedConfigState: session.SharedConfigEnable,
		}))
		svc := ecs.New(sess)
		describeServicesOutput, err := svc.DescribeServices(&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{&service},
		})
		if err != nil {
			return errors.Wrap(err, "cannot describe services")
		}
		if len(describeServicesOutput.Failures) != 0 {
			return fmt.Errorf("Error retrieving information about existing service %v: %v", service, describeServicesOutput.Failures)
		}
		var oldTaskDefinition *string
		for _, existingService := range describeServicesOutput.Services {
			if *existingService.ServiceName == service || *existingService.ServiceArn == service {
				oldTaskDefinition = existingService.TaskDefinition
			}
		}
		if oldTaskDefinition != nil {
			return fmt.Errorf("Service %v already exists in cluster %v. Use czecs upgrade command to upgrade existing service", service, cluster)
		}
		registerTaskDefinitionOutput, err := svc.RegisterTaskDefinition(registerTaskDefinitionInput)
		if err != nil {
			return errors.Wrap(err, "cannot register task definition")
		}
		taskDefn := registerTaskDefinitionOutput.TaskDefinition
		err = deployInstall(svc, cluster, service, *taskDefn.TaskDefinitionArn)
		if err != nil && rollback {
			err = rollbackInstall(svc, cluster, service)
			if err != nil {
				return errors.Wrap(err, "cannot rollback install")
			}
			svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
				TaskDefinition: taskDefn.TaskDefinitionArn,
			})
		}
		return nil
	},
}

func deployInstall(svc *ecs.ECS, cluster string, service string, taskDefnArn string) error {
	// Get the primary deployment's updated date, default to now if missing
	createdAt := time.Now()
	createServiceOutput, err := svc.CreateService(&ecs.CreateServiceInput{
		Cluster:        &cluster,
		ServiceName:    &service,
		TaskDefinition: &taskDefnArn,
	})
	if err != nil {
		// TODO(mbarrien) Avoid rollback?
		return err
	}
	for _, deployment := range createServiceOutput.Service.Deployments {
		if *deployment.Status == "PRIMARY" {
			createdAt = *deployment.CreatedAt
			break
		}
	}

	opts := []request.WaiterOption{getFailOnAbortContext(createdAt)}
	if !quiet {
		opts = append(opts, sleepProgressWithContext)
	}
	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{createServiceOutput.Service.ServiceArn}},
		opts...)
}

func rollbackInstall(svc *ecs.ECS, cluster string, service string) error {
	// Get the primary deployment's updated date, default to now if missing
	deleteServiceOutput, err := svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: &cluster,
		Service: &service,
	})
	if err != nil {
		return err
	}

	opts := []request.WaiterOption{}
	if !quiet {
		opts = append(opts, sleepProgressWithContext)
	}
	return svc.WaitUntilServicesInactiveWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{deleteServiceOutput.Service.ServiceArn}},
		opts...)
}

func init() {
	rootCmd.AddCommand(installCmd)

	f := installCmd.Flags()
	f.BoolVar(&strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVarP(&quiet, "quiet", "q", false, "do not output to console; use return code to determine success/failure")
	f.BoolVar(&rollback, "rollback", false, "delete service if deployment failed")
	f.StringVarP(&service, "name", "n", "", "service name; required for now")
	installCmd.MarkFlagRequired("name")
}
